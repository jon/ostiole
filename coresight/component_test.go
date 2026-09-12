package coresight_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
)

type componentMemory struct {
	words   map[uint64]uint32
	reads   []uint64
	failAt  int
	failure error
	cancel  context.CancelFunc
}

func (m *componentMemory) ReadScalar(ctx context.Context, address uint64, size dap.TransferSize) (uint64, error) {
	if size != dap.Size32 {
		return 0, fmt.Errorf("unexpected transfer size: %v", size)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.reads = append(m.reads, address)
	if len(m.reads) == m.failAt {
		return 0, m.failure
	}
	word, ok := m.words[address]
	if !ok {
		return 0, fmt.Errorf("unmapped register %#x", address)
	}
	if m.cancel != nil {
		m.cancel()
	}
	return uint64(word), nil
}

func memoryAt(base uint64, class uint8) *componentMemory {
	m := &componentMemory{words: map[uint64]uint32{}}
	for i, b := range []uint32{0x0d, uint32(class) << 4, 5, 0xb1} {
		m.words[base+0xff0+uint64(i)*4] = b
	}
	// Arm part 0x906, revision 3, REVAND 5, CMOD 2; SIZE is retained as encoded.
	for i, b := range []uint32{6, 0xb9, 0x3b, 0x52, 0x24, 0, 0, 0} {
		offset := uint64(0xfe0 + i*4)
		if i >= 4 {
			offset = uint64(0xfd0 + (i-4)*4)
		}
		m.words[base+offset] = b
	}
	if class == 9 {
		m.words[base+0xfbc] = 0x47721a14
		m.words[base+0xfc8] = 0x12345678
		m.words[base+0xfcc] = 0x14
	}
	return m
}

func TestIdentify(t *testing.T) {
	for _, base := range []uint64{0, 0xe00ff000, 0x100000000, math.MaxUint64 - 0xfff} {
		for _, class := range []uint8{0, 1, 9, 0xe, 0xf, 7} {
			t.Run(fmt.Sprintf("%x/class%d", base, class), func(t *testing.T) {
				m := memoryAt(base, class)
				got, err := coresight.Identify(t.Context(), m, base)
				if err != nil {
					t.Fatal(err)
				}
				if got.Base != base || got.CIDR != 0xb105000d|uint32(class)<<12 || got.PIDR != 0x24523bb906 {
					t.Fatalf("identity = %+v", got)
				}
				designer, jedec := got.Designer()
				if got.Class() != class || got.Part() != 0x906 || designer != 0x23b || !jedec || got.Revision() != 3 {
					t.Fatalf("decoded identity = %+v, designer=%#x/%v", got, designer, jedec)
				}
				checkDeviceFields(t, got, len(m.reads))
			})
		}
	}
}

func TestIdentityOptionalFields(t *testing.T) {
	m := memoryAt(0, 9)
	m.words[0xfe8] &^= 8
	m.words[0xfbc] &^= 1 << 20
	got, err := coresight.Identify(t.Context(), m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, jedec := got.Designer(); jedec {
		t.Fatal("legacy designer reported as JEP106")
	}
	if _, present := got.Architecture(); present {
		t.Fatal("absent architecture reported as present")
	}
	if got.DEVARCH == 0 {
		t.Fatal("raw DEVARCH lost")
	}
	var zero coresight.Component
	if _, present := zero.Architecture(); present {
		t.Fatal("zero component has architecture")
	}
}

func TestIdentifyRejectsInvalidInputBeforeReads(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		base uint64
	}{
		{"nil context", nil, 0}, {"canceled", canceled, 0}, {"unaligned", t.Context(), 1}, {"overflow", t.Context(), math.MaxUint64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := memoryAt(0, 1)
			_, err := coresight.Identify(tt.ctx, m, tt.base)
			if err == nil || len(m.reads) != 0 {
				t.Fatalf("err=%v, reads=%v", err, m.reads)
			}
		})
	}
	if _, err := coresight.Identify(t.Context(), nil, 0); err == nil {
		t.Fatal("nil reader accepted")
	}
}

func TestIdentifyRejectsBadPreambleBeforePeripheralReads(t *testing.T) {
	for i := range 4 {
		m := memoryAt(0x1000, 9)
		m.words[0x1ff0+uint64(i)*4] ^= 1
		got, err := coresight.Identify(t.Context(), m, 0x1000)
		if err == nil || !strings.Contains(err.Error(), "0x1000") || got != (coresight.Component{}) {
			t.Fatalf("result=%+v, err=%v", got, err)
		}
		if len(m.reads) != 4 {
			t.Fatalf("reads=%v", m.reads)
		}
	}
}

func TestIdentifyIgnoresReservedUpperIDBits(t *testing.T) {
	m := memoryAt(0, 1)
	for address := range m.words {
		m.words[address] |= 0xffffff00
	}
	got, err := coresight.Identify(t.Context(), m, 0)
	if err != nil || got.CIDR != 0xb105100d || got.PIDR != 0x24523bb906 {
		t.Fatalf("result=%+v, err=%v", got, err)
	}
}

func TestIdentifyStopsAtEveryReadFailure(t *testing.T) {
	failure := errors.New("memory inaccessible")
	for i := 1; i <= 15; i++ {
		m := memoryAt(0x1000, 9)
		m.failAt = i
		m.failure = failure
		got, err := coresight.Identify(t.Context(), m, 0x1000)
		address := fmt.Sprintf("%#x", m.reads[len(m.reads)-1])
		if !errors.Is(err, failure) || !strings.Contains(err.Error(), address) || len(m.reads) != i || got != (coresight.Component{}) {
			t.Fatalf("read %d: result=%+v, err=%v, reads=%v", i, got, err, m.reads)
		}
	}
}

func TestIdentifyCancellationBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := memoryAt(0, 9)
	m.cancel = cancel
	_, err := coresight.Identify(ctx, m, 0)
	if !errors.Is(err, context.Canceled) || len(m.reads) != 1 {
		t.Fatalf("err=%v, reads=%v", err, m.reads)
	}
}

func checkDeviceFields(t *testing.T, got coresight.Component, reads int) {
	t.Helper()
	arch, present := got.Architecture()
	if got.Class() == 9 {
		if reads != 15 || !present || arch.Architect != 0x23b || arch.ID != 0x1a14 || arch.Revision != 2 || got.DEVID != 0x12345678 || got.DEVTYPE != 0x14 {
			t.Fatalf("class 9 identity = %+v, architecture = %+v/%v, reads=%d", got, arch, present, reads)
		}
	} else if reads != 12 || present || got.DEVARCH != 0 || got.DEVID != 0 || got.DEVTYPE != 0 {
		t.Fatalf("class %d read class 9 registers: %+v, reads=%d", got.Class(), got, reads)
	}
}
