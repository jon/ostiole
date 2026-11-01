package main

import (
	"context"
	"errors"
	"testing"
)

var errControlCleanup = errors.New("target cleanup failed")

type cleanupTarget struct {
	calls     int
	errors    []error
	onRelease func()
}

func (c *cleanupTarget) Release(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("cleanup needs a fresh deadline")
	}
	c.calls++
	if c.onRelease != nil {
		c.onRelease()
	}
	if c.calls <= len(c.errors) {
		return c.errors[c.calls-1]
	}
	return nil
}

type cleanupConnection struct {
	possible bool
	calls    int
	errors   []error
}

func (c *cleanupConnection) RestorationPossible() bool { return c.possible }
func (c *cleanupConnection) Close() error {
	c.calls++
	if c.calls <= len(c.errors) {
		return c.errors[c.calls-1]
	}
	c.possible = false
	return nil
}

func TestControlCleanupRetainsLiveTargetDependencies(t *testing.T) {
	core := &cleanupTarget{errors: []error{errControlCleanup, errControlCleanup, errControlCleanup}}
	connection := &cleanupConnection{possible: true}
	if err := release(core, connection); !errors.Is(err, errControlCleanup) || core.calls != 3 || connection.calls != 0 {
		t.Fatalf("live cleanup=%v target=%d connection=%d", err, core.calls, connection.calls)
	}
}

func TestControlCleanupRetainsResolvedErrors(t *testing.T) {
	hostErr := errors.New("host cleanup failed once")
	core := &cleanupTarget{errors: []error{errControlCleanup}}
	connection := &cleanupConnection{possible: true, errors: []error{hostErr}}
	if err := release(core, connection); !errors.Is(err, errControlCleanup) || !errors.Is(err, hostErr) || core.calls != 2 || connection.calls != 2 {
		t.Fatalf("resolved cleanup=%v target=%d connection=%d", err, core.calls, connection.calls)
	}
}

func TestControlCleanupUsesTerminalOwnerContract(t *testing.T) {
	for _, duringRelease := range []bool{false, true} {
		terminalErr := errors.New("terminal restoration and host report")
		core := &cleanupTarget{errors: []error{errControlCleanup}}
		connection := &cleanupConnection{possible: duringRelease, errors: []error{terminalErr}}
		core.onRelease = func() { connection.possible = false }
		err := release(core, connection)
		wantCalls := 0
		if duringRelease {
			wantCalls = 1
		}
		if !errors.Is(err, terminalErr) || core.calls != wantCalls || connection.calls != 2 {
			t.Fatalf("terminal cleanup=%v target=%d/%d connection=%d", err, core.calls, wantCalls, connection.calls)
		}
		if duringRelease && !errors.Is(err, errControlCleanup) {
			t.Fatal("lost target error when terminal cleanup proceeded")
		}
	}
}
