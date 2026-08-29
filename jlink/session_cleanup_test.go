package jlink_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/jlink"
	"github.com/jon/ostiole/usb"
)

const jlinkOwnerCloseAttempts = 3

var errJLinkHILUnavailable = errors.New("J-Link HIL unavailable")

type jlinkCleanupStep struct {
	description string
	needsSWD    bool
	release     func(context.Context) error
}

type jlinkCleanup struct {
	swdReady  func() bool
	configure func(context.Context) error
	steps     []jlinkCleanupStep
	abandoned error
}

func newJLinkCleanup(swdReady func() bool, configure func(context.Context) error) *jlinkCleanup {
	return &jlinkCleanup{swdReady: swdReady, configure: configure}
}

func (c *jlinkCleanup) retain(description string, needsSWD bool, release func(context.Context) error) {
	c.steps = append(c.steps, jlinkCleanupStep{description: description, needsSWD: needsSWD, release: release})
}

func (c *jlinkCleanup) releaseCurrent(ctx context.Context) error {
	if len(c.steps) == 0 {
		return nil
	}
	step := c.steps[len(c.steps)-1]
	if step.needsSWD && c.swdReady != nil && !c.swdReady() {
		if c.configure == nil {
			return fmt.Errorf("configure SWD before releasing %s", step.description)
		}
		if err := c.configure(ctx); err != nil {
			err = fmt.Errorf("configure SWD before releasing %s: %w", step.description, err)
			if errors.Is(err, jlink.ErrSessionPoisoned) {
				c.abandonCurrent(err)
				return err
			}
			return err
		}
	}
	if err := step.release(ctx); err != nil {
		err = fmt.Errorf("release %s: %w", step.description, err)
		if step.needsSWD && errors.Is(err, jlink.ErrSessionPoisoned) {
			c.abandonCurrent(err)
			return err
		}
		return err
	}
	c.steps = c.steps[:len(c.steps)-1]
	return nil
}

func (c *jlinkCleanup) abandonCurrent(err error) {
	c.abandoned = errors.Join(c.abandoned, err)
	c.steps = c.steps[:len(c.steps)-1]
}

func (c *jlinkCleanup) release(ctx context.Context) error {
	for len(c.steps) != 0 {
		retained := len(c.steps)
		if err := c.releaseCurrent(ctx); err != nil {
			if len(c.steps) < retained && errors.Is(err, jlink.ErrSessionPoisoned) {
				continue
			}
			return errors.Join(c.abandoned, err)
		}
	}
	return c.abandoned
}

func releaseJLinkCleanup(t *testing.T, cleanup *jlinkCleanup) bool {
	t.Helper()
	var err error
	attempts := 0
	for range jlinkOwnerCloseAttempts {
		attempts++
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = cleanup.release(cleanupCtx)
		cancel()
		if err == nil {
			return true
		}
		if len(cleanup.steps) == 0 {
			break
		}
	}
	t.Errorf("release J-Link owners after %d attempts: %v", attempts, err)
	return false
}

func selectJLinkHILCandidate(candidates []usb.DeviceInfo, selection, serial string) (usb.DeviceInfo, error) {
	if selection != "" && serial != "" {
		return usb.DeviceInfo{}, errors.New("set only one of OSTIOLE_JLINK_HIL_DEVICE and OSTIOLE_JLINK_HIL_SERIAL")
	}
	if serial != "" {
		var matches []usb.DeviceInfo
		for _, candidate := range candidates {
			if candidate.Serial == serial {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return usb.DeviceInfo{}, fmt.Errorf("%w: found %d supported J-Links with the requested serial, want one", errJLinkHILUnavailable, len(matches))
		}
		return matches[0], nil
	}
	if selection == "" {
		if len(candidates) != 1 {
			return usb.DeviceInfo{}, fmt.Errorf("%w: found %d supported J-Links, want one or an exact HIL device or serial selector", errJLinkHILUnavailable, len(candidates))
		}
		return candidates[0], nil
	}
	parts := strings.Split(selection, ":")
	if len(parts) != 2 {
		return usb.DeviceInfo{}, fmt.Errorf("invalid OSTIOLE_JLINK_HIL_DEVICE %q, want bus:address", selection)
	}
	bus, busErr := strconv.ParseUint(parts[0], 10, 8)
	address, addressErr := strconv.ParseUint(parts[1], 10, 8)
	if busErr != nil || addressErr != nil {
		return usb.DeviceInfo{}, fmt.Errorf("invalid OSTIOLE_JLINK_HIL_DEVICE %q, want bus:address", selection)
	}
	for _, candidate := range candidates {
		if candidate.Bus == uint8(bus) && candidate.Address == uint8(address) {
			return candidate, nil
		}
	}
	return usb.DeviceInfo{}, fmt.Errorf("%w: J-Link %s is not present in the supported inventory", errJLinkHILUnavailable, selection)
}

func closeJLinkOwner(t *testing.T, owner interface{ Close() error }, description string) bool {
	t.Helper()
	var err error
	for range jlinkOwnerCloseAttempts {
		if err = owner.Close(); err == nil {
			return true
		}
	}
	t.Errorf("close %s after %d attempts: %v", description, jlinkOwnerCloseAttempts, err)
	return false
}

type retryingJLinkOwner struct {
	calls    int
	failures int
}

func (o *retryingJLinkOwner) Close() error {
	o.calls++
	if o.calls <= o.failures {
		return errors.New("release failed")
	}
	return nil
}

func TestJLinkOwnerCleanupRetries(t *testing.T) {
	owner := &retryingJLinkOwner{failures: 2}
	if !closeJLinkOwner(t, owner, "test owner") {
		return
	}
	if owner.calls != jlinkOwnerCloseAttempts {
		t.Fatalf("Close calls = %d, want %d", owner.calls, jlinkOwnerCloseAttempts)
	}
}

func TestJLinkCleanupRetainsHigherOwnerUntilReleaseSucceeds(t *testing.T) {
	events := []string{}
	failConnection := true
	cleanup := newJLinkCleanup(nil, nil)
	cleanup.retain("session", false, func(context.Context) error {
		events = append(events, "session")
		return nil
	})
	cleanup.retain("connection", true, func(context.Context) error {
		events = append(events, "connection")
		if failConnection {
			failConnection = false
			return errors.New("release failed")
		}
		return nil
	})
	if err := cleanup.release(context.Background()); err == nil {
		t.Fatal("first release succeeded")
	}
	if !reflect.DeepEqual(events, []string{"connection"}) {
		t.Fatalf("first release order = %v", events)
	}
	if err := cleanup.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"connection", "connection", "session"}) {
		t.Fatalf("retry order = %v", events)
	}
}

func TestJLinkCleanupReconfiguresBeforeRetryingSWDOwner(t *testing.T) {
	events := []string{}
	configured := true
	connectionCalls := 0
	cleanup := newJLinkCleanup(func() bool { return configured }, func(context.Context) error {
		events = append(events, "configure")
		configured = true
		return nil
	})
	cleanup.retain("session", false, func(context.Context) error {
		events = append(events, "session")
		return nil
	})
	cleanup.retain("connection", true, func(context.Context) error {
		events = append(events, "connection")
		connectionCalls++
		if connectionCalls == 1 {
			configured = false
			return &jlink.ScanError{Status: 6}
		}
		return nil
	})
	if err := cleanup.release(context.Background()); err == nil {
		t.Fatal("first release succeeded")
	}
	if !reflect.DeepEqual(events, []string{"connection"}) {
		t.Fatalf("first release order = %v", events)
	}
	if err := cleanup.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"connection", "configure", "connection", "session"}) {
		t.Fatalf("retry order = %v", events)
	}
}

func TestJLinkCleanupAbandonsPoisonedOwnerAndRetriesSessionClose(t *testing.T) {
	events := []string{}
	sessionCalls := 0
	cleanup := newJLinkCleanup(func() bool { return true }, nil)
	cleanup.retain("session", false, func(context.Context) error {
		events = append(events, "session")
		sessionCalls++
		if sessionCalls == 1 {
			return errors.New("close failed")
		}
		return nil
	})
	cleanup.retain("connection", true, func(context.Context) error {
		events = append(events, "connection")
		return errors.Join(errors.New("release failed"), jlink.ErrSessionPoisoned)
	})
	if err := cleanup.release(context.Background()); !errors.Is(err, jlink.ErrSessionPoisoned) || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("first release error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"connection", "session"}) {
		t.Fatalf("first release order = %v", events)
	}
	if err := cleanup.release(context.Background()); !errors.Is(err, jlink.ErrSessionPoisoned) || strings.Contains(err.Error(), "close failed") {
		t.Fatalf("second release error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"connection", "session", "session"}) {
		t.Fatalf("retry order = %v", events)
	}
	if len(cleanup.steps) != 0 {
		t.Fatalf("retained cleanup steps = %d", len(cleanup.steps))
	}
}

func TestJLinkCleanupReleaseCurrentReportsPoisonedOwner(t *testing.T) {
	for _, phase := range []string{"reconfiguration", "release"} {
		t.Run(phase, func(t *testing.T) {
			cleanup := newJLinkCleanup(func() bool { return phase == "release" }, func(context.Context) error {
				if phase != "reconfiguration" {
					t.Fatal("configure called")
				}
				return jlink.ErrSessionPoisoned
			})
			cleanup.retain("connection", true, func(context.Context) error {
				if phase != "release" {
					t.Fatal("release called")
				}
				return jlink.ErrSessionPoisoned
			})
			if err := cleanup.releaseCurrent(context.Background()); !errors.Is(err, jlink.ErrSessionPoisoned) {
				t.Fatalf("releaseCurrent() error = %v", err)
			}
			if len(cleanup.steps) != 0 || !errors.Is(cleanup.abandoned, jlink.ErrSessionPoisoned) {
				t.Fatalf("cleanup after poison = steps %d, abandoned %v", len(cleanup.steps), cleanup.abandoned)
			}
		})
	}
}

func TestJLinkCleanupRetriesWithFreshBoundedContexts(t *testing.T) {
	attempts := 0
	cleanup := newJLinkCleanup(nil, nil)
	cleanup.retain("session", false, func(ctx context.Context) error {
		attempts++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("cleanup context has no deadline")
		}
		if attempts < jlinkOwnerCloseAttempts {
			return errors.New("release failed")
		}
		return nil
	})
	if !releaseJLinkCleanup(t, cleanup) {
		return
	}
	if attempts != jlinkOwnerCloseAttempts {
		t.Fatalf("release attempts = %d, want %d", attempts, jlinkOwnerCloseAttempts)
	}
}

func TestSelectJLinkHILCandidate(t *testing.T) {
	candidates := []usb.DeviceInfo{
		{VID: jlink.VID, PID: 0x1020, Bus: 1, Address: 3, Serial: "first"},
		{VID: jlink.VID, PID: 0x1020, Bus: 2, Address: 1, Serial: "second"},
	}
	if got, err := selectJLinkHILCandidate(candidates[:1], "", ""); err != nil || got != candidates[0] {
		t.Fatalf("single candidate = %#v, %v", got, err)
	}
	if _, err := selectJLinkHILCandidate(candidates, "", ""); !errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("ambiguous selection error = %v", err)
	}
	if got, err := selectJLinkHILCandidate(candidates, "2:1", ""); err != nil || got != candidates[1] {
		t.Fatalf("explicit candidate = %#v, %v", got, err)
	}
	if got, err := selectJLinkHILCandidate(candidates, "", "second"); err != nil || got != candidates[1] {
		t.Fatalf("serial candidate = %#v, %v", got, err)
	}
	if _, err := selectJLinkHILCandidate(candidates, "2:1", "second"); err == nil || errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("combined selection error = %v", err)
	}
	if _, err := selectJLinkHILCandidate(candidates, "bad", ""); err == nil || errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("malformed selection error = %v", err)
	}
	if _, err := selectJLinkHILCandidate(candidates, "2:9", ""); !errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("missing exact selection error = %v", err)
	}
	if _, err := selectJLinkHILCandidate(candidates, "", "missing"); !errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("missing serial selection error = %v", err)
	}
	duplicates := append(append([]usb.DeviceInfo(nil), candidates...), usb.DeviceInfo{VID: jlink.VID, PID: 0x1020, Bus: 3, Address: 1, Serial: "second"})
	if _, err := selectJLinkHILCandidate(duplicates, "", "second"); !errors.Is(err, errJLinkHILUnavailable) {
		t.Fatalf("duplicate serial selection error = %v", err)
	}
}
