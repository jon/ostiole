package swd

import (
	"errors"
	"slices"
	"testing"
)

func TestPackedChunkHandlesWAITAfterParity(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "cleanup failure"}[cleanupFailure], func(t *testing.T) {
			primary := errors.New("cleanup transport failed")
			wire := &batchWire{limit: 162, acks: []byte{1, 2, 4}, values: []uint32{1}, corruptParity: map[int]bool{0: true}}
			if cleanupFailure {
				wire.failCall = 2
				wire.err = primary
			}
			conn := readyConn(wire)
			conn.response = responseOverrun
			batch := conn.NewBatch()
			badParity := batch.ReadDP(0)
			waited := batch.ReadAP(0)
			abandoned := batch.ReadAP(0)
			err := batch.Commit(t.Context())
			if !errors.Is(err, ErrParity) || !errors.Is(err, ErrWait) || cleanupFailure && !errors.Is(err, primary) {
				t.Errorf("Commit error = %v", err)
			}
			if _, err := badParity.Value(); !errors.Is(err, ErrParity) {
				t.Errorf("parity result = %v", err)
			}
			if _, err := waited.Value(); !errors.Is(err, ErrWait) || cleanupFailure && !errors.Is(err, primary) {
				t.Errorf("WAIT result = %v", err)
			}
			if _, err := abandoned.Value(); !errors.Is(err, ErrFault) {
				t.Errorf("abandoned result = %v", err)
			}
			if !slices.Equal(wire.calls, []int{162, 54}) {
				t.Fatalf("calls = %v, want chunk then one cleanup", wire.calls)
			}
			if cleanupFailure != (conn.state == connectionRepair) {
				t.Errorf("connection state = %v", conn.state)
			}
		})
	}
}

func TestPackedChunkMarksProtocolAfterEarlierError(t *testing.T) {
	for _, first := range []error{ErrParity, ErrWait} {
		t.Run(first.Error(), func(t *testing.T) {
			wire := &batchWire{limit: 162, acks: []byte{1, 0, 1}, values: []uint32{1}, corruptParity: map[int]bool{0: true}}
			if first == ErrWait {
				wire.acks[0] = 2
			}
			conn := readyConn(wire)
			conn.response = responseOverrun
			batch := conn.NewBatch()
			prefix := batch.ReadDP(0)
			protocol := batch.ReadAP(0)
			suffix := batch.WriteAP(0, 42)
			err := batch.Commit(t.Context())
			if !errors.Is(err, first) || !errors.Is(err, ErrProtocol) {
				t.Errorf("Commit = %v", err)
			}
			if _, err := prefix.Value(); !errors.Is(err, first) || errors.Is(err, ErrIndeterminate) {
				t.Errorf("confirmed prefix = %v", err)
			}
			if _, err := protocol.Value(); !errors.Is(err, ErrIndeterminate) || !errors.Is(err, ErrProtocol) {
				t.Errorf("protocol result = %v", err)
			}
			if err := suffix.Err(); !errors.Is(err, ErrIndeterminate) || !errors.Is(err, ErrProtocol) {
				t.Errorf("uncertain suffix = %v", err)
			}
			before := len(wire.calls)
			if _, err := conn.ReadDP(t.Context(), 0); err == nil || len(wire.calls) != before {
				t.Fatal("protocol loss allowed more traffic")
			}
		})
	}
}

func TestPackedChunkChecksAbandonedSuffixAfterParity(t *testing.T) {
	wire := &batchWire{limit: 162, acks: []byte{1, 2, 1}, values: []uint32{1}, corruptParity: map[int]bool{0: true}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	batch.ReadDP(0)
	batch.ReadAP(0)
	unexpected := batch.WriteAP(0, 42)
	err := batch.Commit(t.Context())
	if !errors.Is(err, ErrParity) || !errors.Is(err, ErrWait) {
		t.Errorf("Commit = %v", err)
	}
	if err := unexpected.Err(); !errors.Is(err, ErrIndeterminate) {
		t.Errorf("unexpected write = %v", err)
	}
	before := len(wire.calls)
	if _, err := conn.ReadDP(t.Context(), 0); err == nil || len(wire.calls) != before {
		t.Fatal("inconsistent suffix allowed more traffic")
	}
}

func TestPackedChunkRequiresBankZeroForStickyExemption(t *testing.T) {
	wire := &batchWire{limit: 216, acks: []byte{1, 1, 2, 1}, values: []uint32{0, 0, 0, 42}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	selected := batch.WriteDP(8, 1)
	batch.ReadDP(12)
	batch.ReadAP(0)
	banked := batch.ReadDP(4)
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrWait) || !errors.Is(err, ErrIndeterminate) {
		t.Errorf("Commit=%v", err)
	}
	if selected.Err() != nil {
		t.Errorf("confirmed SELECT=%v", selected.Err())
	}
	if _, err := banked.Value(); !errors.Is(err, ErrIndeterminate) {
		t.Errorf("bank-one read=%v", err)
	}
	if !slices.Equal(wire.calls, []int{216}) || conn.state != connectionRepair {
		t.Fatalf("calls=%v state=%v", wire.calls, conn.state)
	}
}

func TestPackedChunkErrorIndicesFollowConfirmedPrefix(t *testing.T) {
	wire := &batchWire{limit: 162, acks: []byte{1, 1, 1, 1, 2, 4}, values: []uint32{1, 2, 3, 4}, corruptParity: map[int]bool{3: true}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	prefix := []*ReadResult{batch.ReadDP(0), batch.ReadDP(0), batch.ReadDP(0)}
	parity := batch.ReadDP(0)
	waited := batch.ReadAP(0)
	abandoned := batch.ReadAP(0)
	unsent := batch.ReadAP(0)
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrParity) || !errors.Is(err, ErrWait) {
		t.Errorf("Commit=%v", err)
	}
	for index, result := range prefix {
		if value, err := result.Value(); err != nil || value != uint32(index+1) {
			t.Errorf("prefix%d=%#x %v", index, value, err)
		}
	}
	for index, result := range []*ReadResult{parity, waited, abandoned, unsent} {
		want := []error{ErrParity, ErrWait, ErrFault, ErrNotExecuted}[index]
		if _, err := result.Value(); !errors.Is(err, want) {
			t.Errorf("result%d=%v want%v", index, err, want)
		}
	}
	if !slices.Equal(wire.calls, []int{162, 162, 54}) {
		t.Fatalf("calls=%v", wire.calls)
	}
}
