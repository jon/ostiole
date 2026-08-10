package swd

import (
	"context"
	"errors"
	"testing"
)

type targetWire struct {
	ack           byte
	readValue     uint32
	corruptParity bool
	request       Request
	written       uint32
	calls         int
	cleanupErr    error
}

func (w *targetWire) SWDIO(_ context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if w.calls == 1 {
		return w.requestPhase(direction, output, bits)
	}
	if w.calls == 2 {
		return w.dataPhase(direction, output, bits)
	}
	return nil, errors.New("unexpected SWD phase")
}

func (w *targetWire) requestPhase(direction, output []byte, bits int) ([]byte, error) {
	if bits != 12 || !allTestBits(direction, 0, 8, true) ||
		!allTestBits(direction, 8, 12, false) {
		return nil, errors.New("invalid request direction")
	}
	header := output[0]
	if header&0x81 != 0x81 || header&0x40 != 0 {
		return nil, errors.New("invalid request framing")
	}
	w.request = Request{
		AP:   header&0x02 != 0,
		Read: header&0x04 != 0,
		Addr: (header >> 1) & 0x0c,
	}
	fields := header >> 1 & 0x0f
	if testParity32(uint32(fields)) != (header&0x20 != 0) {
		return nil, errors.New("invalid request parity")
	}
	input := make([]byte, 2)
	for bit := range 3 {
		setTestBit(input, 9+bit, w.ack>>uint(bit)&1 != 0)
	}
	return input, nil
}

func (w *targetWire) dataPhase(direction, output []byte, bits int) ([]byte, error) {
	if w.ack != 0b001 {
		if bits != 9 || !allTestBits(direction, 1, 9, true) {
			return nil, errors.New("invalid failed-ACK cleanup")
		}
		if w.cleanupErr != nil {
			return nil, w.cleanupErr
		}
		return make([]byte, (bits+7)/8), nil
	}
	if bits != 42 {
		return nil, errors.New("invalid data-phase length")
	}
	if w.request.Read {
		return w.readPhase(direction, bits)
	}
	return w.writePhase(direction, output, bits)
}

func (w *targetWire) readPhase(direction []byte, bits int) ([]byte, error) {
	if !allTestBits(direction, 0, 34, false) ||
		!allTestBits(direction, 34, bits, true) {
		return nil, errors.New("invalid read direction")
	}
	input := make([]byte, (bits+7)/8)
	for bit := range 32 {
		setTestBit(input, bit, w.readValue>>uint(bit)&1 != 0)
	}
	parity := testParity32(w.readValue)
	setTestBit(input, 32, parity != w.corruptParity)
	return input, nil
}

func (w *targetWire) writePhase(direction, output []byte, bits int) ([]byte, error) {
	if testBit(direction, 0) ||
		!allTestBits(direction, 1, bits, true) {
		return nil, errors.New("invalid write direction")
	}
	for bit := range 32 {
		if testBit(output, bit+1) {
			w.written |= 1 << uint(bit)
		}
	}
	if testBit(output, 33) != testParity32(w.written) {
		return nil, errors.New("invalid write parity")
	}
	return make([]byte, (bits+7)/8), nil
}

func TestTransferReadsAndWritesOneRegister(t *testing.T) {
	readWire := &targetWire{ack: 0b001, readValue: 0x2ba01477}
	got, err := New(readWire).Transfer(t.Context(), Request{Read: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != readWire.readValue || readWire.calls != 2 {
		t.Fatalf("read = %#08x after %d calls", got, readWire.calls)
	}

	writeWire := &targetWire{ack: 0b001}
	const written = 0x12345678
	if _, err := New(writeWire).Transfer(t.Context(), Request{AP: true, Addr: 0x0c}, written); err != nil {
		t.Fatal(err)
	}
	if writeWire.written != written || writeWire.calls != 2 {
		t.Fatalf("write = %#08x after %d calls", writeWire.written, writeWire.calls)
	}
}

func TestTransferClassifiesAcknowledgements(t *testing.T) {
	tests := []struct {
		ack  byte
		want error
	}{
		{ack: 0b010, want: ErrWait},
		{ack: 0b100, want: ErrFault},
		{ack: 0b000, want: ErrProtocol},
	}
	for _, test := range tests {
		w := &targetWire{ack: test.ack}
		_, err := New(w).Transfer(t.Context(), Request{Read: true}, 0)
		if !errors.Is(err, test.want) {
			t.Fatalf("ACK %03b error = %v", test.ack, err)
		}
	}
}

func TestTransferPreservesAcknowledgementWhenCleanupFails(t *testing.T) {
	cleanupErr := errors.New("injected cleanup failure")
	w := &targetWire{ack: 0b010, cleanupErr: cleanupErr}
	_, err := New(w).Transfer(t.Context(), Request{Read: true}, 0)
	if !errors.Is(err, ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Transfer() error = %v, want WAIT and cleanup failure", err)
	}
}

func TestTransferRejectsInvalidReadParity(t *testing.T) {
	w := &targetWire{
		ack:           0b001,
		readValue:     0x2ba01477,
		corruptParity: true,
	}
	if _, err := New(w).Transfer(t.Context(), Request{Read: true}, 0); !errors.Is(err, ErrParity) {
		t.Fatalf("parity error = %v", err)
	}
}

type shortWire struct{}

func (shortWire) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) {
	return nil, nil
}

func TestTransferRejectsInvalidInputs(t *testing.T) {
	w := &targetWire{ack: 0b001}
	if _, err := New(w).Transfer(t.Context(), Request{Read: true, Addr: 1}, 0); err == nil || w.calls != 0 {
		t.Fatalf("invalid address error = %v after %d calls", err, w.calls)
	}
	if _, err := New(shortWire{}).Transfer(t.Context(), Request{Read: true}, 0); err == nil {
		t.Fatal("short lower-layer response succeeded")
	}
}

func FuzzRequestHeader(f *testing.F) {
	f.Add(false, false, uint8(0))
	f.Add(true, true, uint8(0x0c))
	f.Fuzz(func(t *testing.T, ap, read bool, addr uint8) {
		addr &= 0x0c
		header := requestByte(Request{AP: ap, Read: read, Addr: addr})
		if header&0x81 != 0x81 || header&0x40 != 0 {
			t.Fatalf("framing = %#02x", header)
		}
		fields := header >> 1 & 0x0f
		if testParity32(uint32(fields)) != (header&0x20 != 0) {
			t.Fatalf("parity = %#02x", header)
		}
	})
}

func testBit(buf []byte, bit int) bool {
	return buf[bit/8]>>(uint(bit)%8)&1 != 0
}

func setTestBit(buf []byte, bit int, value bool) {
	if value {
		buf[bit/8] |= 1 << (uint(bit) % 8)
	}
}

func allTestBits(buf []byte, start, end int, value bool) bool {
	for bit := start; bit < end; bit++ {
		if testBit(buf, bit) != value {
			return false
		}
	}
	return true
}

func testParity32(value uint32) bool {
	parity := false
	for value != 0 {
		parity = parity != (value&1 != 0)
		value >>= 1
	}
	return parity
}
