package jlink_test

import (
	"errors"
	"testing"
)

const jlinkOwnerCloseAttempts = 3

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
