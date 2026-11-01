package main

import (
	"context"
	"errors"
	"testing"
)

type protocolCleanup struct {
	calls   int
	err     error
	pending bool
}

func (p *protocolCleanup) CleanupPending() bool { return p.pending }

func (p *protocolCleanup) Release(ctx context.Context) error {
	p.calls++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing cleanup deadline")
	}
	if p.err == nil {
		p.pending = false
	}
	return p.err
}

type channelCleanup struct {
	possible bool
	calls    int
	err      error
}

func (c *channelCleanup) RestorationPossible() bool { return c.possible }
func (c *channelCleanup) Close() error              { c.calls++; return c.err }

func TestCleanupRetainsLiveProtocolDependency(t *testing.T) {
	failure := errors.New("protocol restoration failed")
	port := &protocolCleanup{err: failure, pending: true}
	channel := &channelCleanup{possible: true}
	if err := releaseConnection(port, channel); !errors.Is(err, failure) || port.calls != 1 || channel.calls != 0 {
		t.Fatalf("live cleanup=%v protocol calls=%d host calls=%d", err, port.calls, channel.calls)
	}
	port.err = nil
	if err := releaseConnection(port, channel); err != nil || port.calls != 2 || channel.calls != 1 {
		t.Fatalf("retry cleanup=%v protocol calls=%d host calls=%d", err, port.calls, channel.calls)
	}
}

func TestCleanupTerminalLossDoesNotAttemptProtocol(t *testing.T) {
	port := &protocolCleanup{}
	hostFailure := errors.New("host cleanup failure")
	channel := &channelCleanup{err: hostFailure}
	if err := releaseConnection(port, channel); err == nil || !errors.Is(err, hostFailure) || port.calls != 0 || channel.calls != 1 {
		t.Fatalf("terminal cleanup=%v protocol calls=%d host calls=%d", err, port.calls, channel.calls)
	}
}

func TestCleanupCompletedReleaseErrorClosesHost(t *testing.T) {
	failure := errors.New("completed release reported cancellation")
	port := &protocolCleanup{err: failure}
	channel := &channelCleanup{possible: true}
	if err := releaseConnection(port, channel); !errors.Is(err, failure) || port.calls != 1 || channel.calls != 1 {
		t.Fatalf("completed cleanup=%v protocol=%d host=%d", err, port.calls, channel.calls)
	}
}
