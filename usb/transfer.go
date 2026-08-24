package usb

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const bulkCleanupTimeout = time.Second

type bulkTransferBackend interface {
	completion() <-chan struct{}
	failure() <-chan struct{}
	failureResult() error
	result() (int, error)
}

type bulkTransferEngine interface {
	submit(context.Context, uint8, []byte) (bulkTransferBackend, error)
	abort(uint8) error
	pending() bool
	close() error
}

// BulkTransfer is one submitted USB bulk transfer. Its buffer remains owned by
// the host request until Done is closed.
type BulkTransfer struct {
	backend bulkTransferBackend
}

// Done is closed when the transfer completes or an endpoint abort has drained
// it. Wait then reports the same completion.
func (t *BulkTransfer) Done() <-chan struct{} {
	if t == nil || t.backend == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return t.backend.completion()
}

// Endpoint returns the active descriptor for address in the claimed
// interface's selected alternate setting. The first call reads the current
// alternate setting rather than assuming that a new claim selected zero.
func (c *ClaimedInterface) Endpoint(ctx context.Context, address uint8) (Endpoint, error) {
	if ctx == nil {
		return Endpoint{}, errors.New("usb: nil endpoint context")
	}
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}
	if c == nil || c.device == nil {
		return Endpoint{}, errors.New("usb: claimed interface is closed")
	}
	if c.endpoints == nil {
		if err := c.loadEndpoints(ctx); err != nil {
			return Endpoint{}, fmt.Errorf("usb: inspect claimed interface endpoints: %w", err)
		}
	}
	endpoint, ok := c.endpoints[address]
	if !ok {
		return Endpoint{}, fmt.Errorf("usb: endpoint %#02x is not active", address)
	}
	return endpoint, nil
}

func (c *ClaimedInterface) loadEndpoints(ctx context.Context) error {
	if !c.altKnown {
		alternate, endpoints, err := claimedInterfaceEndpoints(ctx, c.device, c.number)
		if err != nil {
			return err
		}
		c.alternate, c.altKnown, c.endpoints = alternate, true, endpoints
		return nil
	}
	configuration, err := c.device.ActiveConfiguration(ctx)
	if err != nil {
		return err
	}
	c.endpoints, err = selectedEndpoints(configuration, c.number, c.alternate)
	return err
}

func claimedInterfaceEndpoints(ctx context.Context, device controlTransferer, number uint8) (uint8, map[uint8]Endpoint, error) {
	alternate, err := interfaceAlternate(ctx, device, number)
	if err != nil {
		return 0, nil, fmt.Errorf("read interface %d alternate setting: %w", number, err)
	}
	configuration, err := activeConfiguration(ctx, device)
	if err != nil {
		return 0, nil, err
	}
	endpoints, err := selectedEndpoints(configuration, number, alternate)
	if err != nil {
		return 0, nil, err
	}
	return alternate, endpoints, nil
}

func selectedEndpoints(configuration Configuration, number, alternate uint8) (map[uint8]Endpoint, error) {
	for _, iface := range configuration.Interfaces {
		if iface.Number != number {
			continue
		}
		for _, setting := range iface.Alternates {
			if setting.Number != alternate {
				continue
			}
			endpoints := make(map[uint8]Endpoint, len(setting.Endpoints))
			for _, endpoint := range setting.Endpoints {
				endpoints[endpoint.Address] = endpoint
			}
			return endpoints, nil
		}
	}
	return nil, fmt.Errorf("usb: interface %d alternate %d is not active", number, alternate)
}

// SubmitBulk submits one transfer to an active bulk endpoint. The endpoint
// address determines direction, and len(buffer) is the requested transfer
// length. Wait reports the exact count for a successful short or zero-length
// completion. The caller must not access buffer before Done is closed.
// Submission does not pair endpoints, choose buffer sizes, or arrange later
// transfers.
func (c *ClaimedInterface) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (*BulkTransfer, error) {
	if ctx == nil {
		return nil, errors.New("usb: nil bulk-transfer submission context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	descriptor, err := c.Endpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if descriptor.TransferType != TransferBulk {
		return nil, fmt.Errorf("usb: endpoint %#02x is not bulk", endpoint)
	}
	if c.transfers == nil {
		engine, err := c.device.openBulkTransfers(c)
		if err != nil {
			return nil, err
		}
		c.transfers = engine
	}
	backend, err := c.transfers.submit(ctx, endpoint, buffer)
	if err != nil {
		return nil, err
	}
	return &BulkTransfer{backend: backend}, nil
}

// Wait waits for transfer completion. Ending ctx stops waiting without
// aborting the submitted transfer. A host-engine failure can also end the wait
// while Done remains open and the buffer remains owned by the request. On a
// failed completion or host-engine failure, Wait returns a zero count.
func (t *BulkTransfer) Wait(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("usb: nil bulk-transfer wait context")
	}
	if t == nil || t.backend == nil {
		return 0, errors.New("usb: nil bulk transfer")
	}
	done := t.Done()
	if channelClosed(done) {
		return bulkTransferResult(t.backend)
	}
	select {
	case <-done:
		return bulkTransferResult(t.backend)
	case <-t.backend.failure():
		if channelClosed(done) {
			return bulkTransferResult(t.backend)
		}
		return 0, t.backend.failureResult()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func bulkTransferResult(backend bulkTransferBackend) (int, error) {
	count, err := backend.result()
	if err != nil {
		return 0, err
	}
	return count, nil
}

// AbortBulk aborts and drains all pending transfers on endpoint. If native
// cancellation or the bounded drain fails, the pending requests remain owned
// and AbortBulk can be retried. A drain timeout matches context.DeadlineExceeded.
// Abort is endpoint-wide because the supported host APIs do not both cancel an
// individual request.
func (c *ClaimedInterface) AbortBulk(endpoint uint8) error {
	if c == nil || c.device == nil {
		return errors.New("usb: claimed interface is closed")
	}
	if c.transfers == nil {
		return nil
	}
	return c.transfers.abort(endpoint)
}
