package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type cleanupProtocol struct {
	name      string
	calls     *[]string
	err       error
	onRelease func()
	pending   bool
}

func (p *cleanupProtocol) CleanupPending() bool { return p.pending }

func (p *cleanupProtocol) Release(ctx context.Context) error {
	*p.calls = append(*p.calls, p.name)
	if p.onRelease != nil {
		p.onRelease()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing cleanup deadline")
	}
	return p.err
}

type cleanupChannel struct {
	possible bool
	calls    *[]string
	err      error
}

func (c *cleanupChannel) RestorationPossible() bool { return c.possible }
func (c *cleanupChannel) Close() error {
	*c.calls = append(*c.calls, "channel")
	return c.err
}

func TestIdentityCleanupRetainsEachLiveDependency(t *testing.T) {
	for _, fail := range []string{"memory", "port"} {
		t.Run(fail, func(t *testing.T) {
			var calls []string
			failure := errors.New("live cleanup failure")
			memory := &cleanupProtocol{name: "memory", calls: &calls}
			port := &cleanupProtocol{name: "port", calls: &calls, pending: true}
			channel := &cleanupChannel{possible: true, calls: &calls}
			want := []string{"memory"}
			if fail == "memory" {
				memory.err = failure
			} else {
				port.err = failure
				want = append(want, "port")
			}
			if err := releaseIdentity(memory, port, channel); !errors.Is(err, failure) || !reflect.DeepEqual(calls, want) {
				t.Fatalf("cleanup=%v calls=%v want=%v", err, calls, want)
			}
			calls = nil
			memory.err, port.err = nil, nil
			if err := releaseIdentity(memory, port, channel); err != nil || !reflect.DeepEqual(calls, []string{"memory", "port", "channel"}) {
				t.Fatalf("retry=%v calls=%v", err, calls)
			}
		})
	}
}

func TestIdentityTerminalCleanupSkipsProtocolTraffic(t *testing.T) {
	var calls []string
	failure := errors.New("host cleanup failed")
	memory := &cleanupProtocol{name: "memory", calls: &calls}
	port := &cleanupProtocol{name: "port", calls: &calls}
	channel := &cleanupChannel{calls: &calls, err: failure}
	if err := releaseIdentity(memory, port, channel); !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{"channel"}) {
		t.Fatalf("terminal cleanup=%v calls=%v", err, calls)
	}
}

func TestIdentityCleanupTerminalLossDuringMemoryRelease(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var calls []string
		failure := errors.New("memory release lost transport")
		channel := &cleanupChannel{possible: true, calls: &calls}
		memory := &cleanupProtocol{name: "memory", calls: &calls, onRelease: func() { channel.possible = false }}
		if failed {
			memory.err = failure
		}
		port := &cleanupProtocol{name: "port", calls: &calls}
		err := releaseIdentity(memory, port, channel)
		if err == nil || failed && !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{"memory", "channel"}) {
			t.Fatalf("terminal during memory cleanup=%v calls=%v", err, calls)
		}
	}
}

func TestIdentityCompletedPortReleaseErrorClosesHost(t *testing.T) {
	var calls []string
	failure := errors.New("completed port release returned context error")
	memory := &cleanupProtocol{name: "memory", calls: &calls}
	port := &cleanupProtocol{name: "port", calls: &calls, err: failure}
	channel := &cleanupChannel{possible: true, calls: &calls}
	if err := releaseIdentity(memory, port, channel); !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{"memory", "port", "channel"}) {
		t.Fatalf("completed port cleanup=%v calls=%v", err, calls)
	}
}
