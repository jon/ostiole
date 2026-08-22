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
	request       request
	written       uint32
	calls         int
	cleanupErr    error
}

type fixedWire struct {
	ack           byte
	readValue     uint32
	corruptParity bool
	nonOKParity   bool
	request       request
	written       uint32
	calls         int
}

func readyConn(w Wire) *Conn {
	c := New(w)
	c.state = connectionReady
	c.bank = bankSelection{valid: true}
	return c
}

func (w *fixedWire) SWDIO(_ context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if bits != 54 || !allTestBits(direction, 0, 8, true) || !allTestBits(direction, 8, 12, false) {
		return nil, errors.New("invalid fixed frame")
	}
	header := output[0]
	w.request = mustRequest(header&0x02 != 0, header&0x04 != 0, (header>>1)&0x0c)
	input := make([]byte, (bits+7)/8)
	for bit := range 3 {
		setTestBit(input, 9+bit, w.ack>>uint(bit)&1 != 0)
	}
	if w.ack != 0b001 {
		setTestBit(input, 44, w.nonOKParity)
		return input, nil
	}
	if w.request.isRead() {
		if !allTestBits(direction, 12, 46, false) || !allTestBits(direction, 46, 54, true) {
			return nil, errors.New("invalid fixed read direction")
		}
		for bit := range 32 {
			setTestBit(input, 12+bit, w.readValue>>uint(bit)&1 != 0)
		}
		setTestBit(input, 44, testParity32(w.readValue) != w.corruptParity)
		return input, nil
	}
	if !allTestBits(direction, 12, 13, false) || !allTestBits(direction, 13, 54, true) {
		return nil, errors.New("invalid fixed write direction")
	}
	for bit := range 32 {
		if testBit(output, 13+bit) {
			w.written |= 1 << uint(bit)
		}
	}
	if testBit(output, 45) != testParity32(w.written) {
		return nil, errors.New("invalid fixed write parity")
	}
	return input, nil
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
	w.request = mustRequest(header&0x02 != 0, header&0x04 != 0, (header>>1)&0x0c)
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
		if w.ack == 0 && (bits != 42 || !allTestBits(direction, 0, 34, false) || !allTestBits(direction, 34, 42, true)) {
			return nil, errors.New("invalid protocol-error recovery")
		}
		if w.ack != 0 && (bits != 9 || !allTestBits(direction, 1, 9, true)) {
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
	if w.request.isRead() {
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
	got, err := readyConn(readWire).ReadDP(t.Context(), 0x00)
	if err != nil {
		t.Fatal(err)
	}
	if got != readWire.readValue || readWire.calls != 2 {
		t.Fatalf("read = %#08x after %d calls", got, readWire.calls)
	}
	if readWire.request != mustRequest(false, true, 0x00) {
		t.Fatalf("ReadDP() request = %#v", readWire.request)
	}

	writeWire := &targetWire{ack: 0b001}
	const written = 0x12345678
	if err := readyConn(writeWire).WriteAP(t.Context(), 0x0c, written); err != nil {
		t.Fatal(err)
	}
	if writeWire.written != written || writeWire.calls != 2 {
		t.Fatalf("write = %#08x after %d calls", writeWire.written, writeWire.calls)
	}
	if writeWire.request != mustRequest(true, false, 0x0c) {
		t.Fatalf("WriteAP() request = %#v", writeWire.request)
	}

	apReadWire := &targetWire{ack: 0b001}
	if _, err := readyConn(apReadWire).ReadAP(t.Context(), 0x08); err != nil {
		t.Fatal(err)
	}
	if apReadWire.request != mustRequest(true, true, 0x08) {
		t.Fatalf("ReadAP() request = %#v", apReadWire.request)
	}

	dpWriteWire := &targetWire{ack: 0b001}
	if err := readyConn(dpWriteWire).WriteDP(t.Context(), 0x04, written); err != nil {
		t.Fatal(err)
	}
	if dpWriteWire.request != mustRequest(false, false, 0x04) {
		t.Fatalf("WriteDP() request = %#v", dpWriteWire.request)
	}
}

func TestTransferClocksFixedOverrunFrames(t *testing.T) {
	readWire := &fixedWire{ack: 0b001, readValue: 0x2ba01477}
	readConn := readyConn(readWire)
	readConn.response = responseOverrun
	got, err := readConn.ReadDP(t.Context(), 0x00)
	if err != nil {
		t.Fatal(err)
	}
	if got != readWire.readValue || readWire.calls != 1 {
		t.Fatalf("fixed read = %#08x after %d calls", got, readWire.calls)
	}

	writeWire := &fixedWire{ack: 0b001}
	writeConn := readyConn(writeWire)
	writeConn.response = responseOverrun
	const written = 0x12345678
	if err := writeConn.WriteAP(t.Context(), 0x0c, written); err != nil {
		t.Fatal(err)
	}
	if writeWire.written != written || writeWire.calls != 1 {
		t.Fatalf("fixed write = %#08x after %d calls", writeWire.written, writeWire.calls)
	}
}

func TestTransferClocksDataPhaseAfterOverrunError(t *testing.T) {
	for _, test := range []struct {
		ack  byte
		want error
	}{
		{ack: 0b010, want: ErrWait},
		{ack: 0b100, want: ErrFault},
		{ack: 0b000, want: ErrProtocol},
	} {
		wire := &fixedWire{ack: test.ack, nonOKParity: true}
		conn := readyConn(wire)
		conn.response = responseOverrun
		if _, err := conn.ReadDP(t.Context(), 0x00); !errors.Is(err, test.want) {
			t.Fatalf("ACK %03b error = %v, want %v", test.ack, err, test.want)
		}
		wantCalls := 1
		if test.want == ErrWait {
			wantCalls = 2
		}
		if wire.calls != wantCalls {
			t.Fatalf("ACK %03b used %d wire calls, want %d", test.ack, wire.calls, wantCalls)
		}
		if test.want == ErrProtocol {
			before := wire.calls
			if _, err := conn.ReadDP(t.Context(), 0x00); err == nil || wire.calls != before {
				t.Fatalf("fixed ReadDP() after invalid ACK error = %v after %d new calls", err, wire.calls-before)
			}
		}
	}
}

func TestTransferChecksFixedReadParityAfterOK(t *testing.T) {
	wire := &fixedWire{ack: 0b001, readValue: 0x2ba01477, corruptParity: true}
	conn := readyConn(wire)
	conn.response = responseOverrun
	if _, err := conn.ReadDP(t.Context(), 0x00); !errors.Is(err, ErrParity) {
		t.Fatalf("fixed read error = %v, want parity error", err)
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
		conn := readyConn(w)
		_, err := conn.ReadDP(t.Context(), 0x00)
		if !errors.Is(err, test.want) {
			t.Fatalf("ACK %03b error = %v", test.ack, err)
		}
		if test.want == ErrProtocol {
			if err != ErrProtocol {
				t.Fatalf("invalid ACK error = %v, want protocol error without recovery failure", err)
			}
			before := w.calls
			if _, err := conn.ReadDP(t.Context(), 0x00); err == nil || w.calls != before {
				t.Fatalf("ReadDP() after invalid ACK error = %v after %d new calls", err, w.calls-before)
			}
		}
	}
}

func TestTransferPreservesAcknowledgementWhenCleanupFails(t *testing.T) {
	cleanupErr := errors.New("injected cleanup failure")
	w := &targetWire{ack: 0b010, cleanupErr: cleanupErr}
	_, err := readyConn(w).ReadDP(t.Context(), 0x00)
	if !errors.Is(err, ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadDP() error = %v, want WAIT and cleanup failure", err)
	}
}

func TestTransferRejectsInvalidReadParity(t *testing.T) {
	w := &targetWire{
		ack:           0b001,
		readValue:     0x2ba01477,
		corruptParity: true,
	}
	if _, err := readyConn(w).ReadDP(t.Context(), 0x00); !errors.Is(err, ErrParity) {
		t.Fatalf("parity error = %v", err)
	}
}

type shortWire struct{}

func (shortWire) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) {
	return nil, nil
}

func TestTransferRejectsInvalidInputs(t *testing.T) {
	w := &targetWire{ack: 0b001}
	if _, err := New(w).ReadDP(t.Context(), 0x01); err == nil || w.calls != 0 {
		t.Fatalf("invalid address error = %v after %d calls", err, w.calls)
	}
	if _, err := readyConn(shortWire{}).ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("short lower-layer response succeeded")
	}
}

func FuzzRequestHeader(f *testing.F) {
	f.Add(false, false, uint8(0))
	f.Add(true, true, uint8(0x0c))
	f.Fuzz(func(t *testing.T, ap, read bool, addr uint8) {
		addr &= 0x0c
		header := requestByte(mustRequest(ap, read, addr))
		if header&0x81 != 0x81 || header&0x40 != 0 {
			t.Fatalf("framing = %#02x", header)
		}
		fields := header >> 1 & 0x0f
		if testParity32(uint32(fields)) != (header&0x20 != 0) {
			t.Fatalf("parity = %#02x", header)
		}
	})
}

func mustRequest(ap, read bool, addr uint8) request {
	req, err := newRequest(ap, read, addr)
	if err != nil {
		panic(err)
	}
	return req
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
