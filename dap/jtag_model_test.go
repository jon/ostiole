package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/jtag"
)

var tapNext = [17][2]jtag.State{
	jtag.Reset: {jtag.Idle, jtag.Reset}, jtag.Idle: {jtag.Idle, jtag.SelectDR},
	jtag.SelectDR: {jtag.CaptureDR, jtag.SelectIR}, jtag.CaptureDR: {jtag.ShiftDR, jtag.Exit1DR},
	jtag.ShiftDR: {jtag.ShiftDR, jtag.Exit1DR}, jtag.Exit1DR: {jtag.PauseDR, jtag.UpdateDR},
	jtag.PauseDR: {jtag.PauseDR, jtag.Exit2DR}, jtag.Exit2DR: {jtag.ShiftDR, jtag.UpdateDR},
	jtag.UpdateDR: {jtag.Idle, jtag.SelectDR}, jtag.SelectIR: {jtag.CaptureIR, jtag.Reset},
	jtag.CaptureIR: {jtag.ShiftIR, jtag.Exit1IR}, jtag.ShiftIR: {jtag.ShiftIR, jtag.Exit1IR},
	jtag.Exit1IR: {jtag.PauseIR, jtag.UpdateIR}, jtag.PauseIR: {jtag.PauseIR, jtag.Exit2IR},
	jtag.Exit2IR: {jtag.ShiftIR, jtag.UpdateIR}, jtag.UpdateIR: {jtag.Idle, jtag.SelectDR},
}

type dpModelTAP struct {
	ir          int
	id          uint32
	instruction uint64
	dp          *jtagDPModel
}

type jtagDPWire struct {
	taps         []dpModelTAP
	state        jtag.State
	shift        []byte
	calls, limit int
	fail         bool
	failAfter    bool
	expire       bool
}

type jtagRequest struct {
	ap, read bool
	addr     uint8
	data     uint32
}

type jtagDPModel struct {
	ctrl, selectDP   uint32
	pending          *jtagRequest
	ack              byte
	response         uint32
	waits, nextWaits int
	aborts           int
	accepted         []jtagRequest
	complete         func(jtagRequest) uint32
	onAccept         func(jtagRequest)
	onWait           func()
	onAbort          func()
	ignoreControl    bool
	stuckSticky      bool
	forcedACK        *byte
}

func (m *jtagDPModel) capture() uint64 {
	m.ack, m.response = 2, 0
	if m.waits > 0 {
		m.waits--
		m.ack = 1
		if m.onWait != nil {
			m.onWait()
		}
		if m.ctrl&1 != 0 {
			m.ctrl |= 2
		}
	} else if m.pending != nil {
		m.response = m.execute(*m.pending)
		m.pending = nil
	}
	if m.forcedACK != nil {
		m.ack = *m.forcedACK
		m.forcedACK = nil
	}
	return uint64(m.response)<<3 | uint64(m.ack)
}

func (m *jtagDPModel) update(instruction uint64, value uint64) {
	if instruction&0xf == 8 {
		if value != 8 {
			panic("non-baseline ABORT")
		}
		if m.onAbort != nil {
			m.onAbort()
		}
		m.aborts++
		m.ctrl &^= 0xfff << 12
		m.pending, m.waits = nil, 0
		return
	}
	if m.ack == 1 {
		return
	}
	r := jtagRequest{ap: instruction&0xf == 0xb, read: value&1 != 0, addr: uint8(value>>1&3) * 4, data: uint32(value >> 3)}
	m.accepted = append(m.accepted, r)
	m.pending = &r
	if m.onAccept != nil {
		m.onAccept(r)
	}
	if r.ap {
		m.waits, m.nextWaits = m.nextWaits, 0
	}
}

func (m *jtagDPModel) execute(r jtagRequest) uint32 {
	if r.ap {
		if m.complete != nil {
			return m.complete(r)
		}
		return 0
	}
	if r.read {
		switch r.addr {
		case 4:
			return m.ctrl
		case 8:
			return m.selectDP
		case 12:
			return 0
		default:
			panic("undefined JTAG DP read")
		}
	}
	switch r.addr {
	case 4:
		if m.ignoreControl {
			return 0
		}
		sticky := uint32(2 | 1<<4 | 1<<5)
		m.ctrl = (r.data &^ (sticky | 1<<29 | 1<<31)) | (m.ctrl & sticky &^ r.data)
		m.ctrl |= (m.ctrl & (1<<28 | 1<<30)) << 1
		if m.stuckSticky {
			m.ctrl |= 1 << 5
		}
	case 8:
		m.selectDP = r.data
	default:
		panic("undefined JTAG DP write")
	}
	return 0
}

func (w *jtagDPWire) MaxTransferBits() int { return w.limit }
func (w *jtagDPWire) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.calls++
	if w.expire {
		w.expire = false
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("recovery has no deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if w.fail {
		w.fail = false
		return nil, errors.New("injected JTAG failure")
	}
	if bits > w.limit {
		panic("driver transfer limit exceeded")
	}
	out := make([]byte, (bits+7)/8)
	for i := range bits {
		out[i/8] |= w.tick(tms[i/8]>>uint(i%8)&1, tdi[i/8]>>uint(i%8)&1) << uint(i%8)
	}
	if w.failAfter {
		w.failAfter = false
		return nil, errors.New("injected JTAG failure after traffic")
	}
	return out, nil
}

func (w *jtagDPWire) tick(tms, tdi byte) byte {
	var out byte
	switch w.state {
	case jtag.Reset:
		for i := range w.taps {
			w.taps[i].instruction = uint64(0xe) | ((uint64(1)<<uint(w.taps[i].ir))-1)&^0xf
		}
	case jtag.CaptureIR, jtag.CaptureDR:
		w.capture()
	case jtag.ShiftIR, jtag.ShiftDR:
		out = w.shift[0]
		copy(w.shift, w.shift[1:])
		w.shift[len(w.shift)-1] = tdi
	case jtag.UpdateIR, jtag.UpdateDR:
		w.update()
	}
	w.state = tapNext[w.state][tms]
	return out
}

func (w *jtagDPWire) capture() {
	w.shift = nil
	for _, tap := range w.taps {
		bits, value := 1, uint64(0)
		switch {
		case w.state == jtag.CaptureIR:
			bits, value = tap.ir, 1
		case tap.instruction&0xf == 0xe:
			bits, value = 32, uint64(tap.id)
		case tap.dp != nil && (tap.instruction&0xf == 0xa || tap.instruction&0xf == 0xb):
			bits, value = 35, tap.dp.capture()
		case tap.dp != nil && tap.instruction&0xf == 8:
			bits = 35
		}
		for i := range bits {
			w.shift = append(w.shift, byte(value>>uint(i)&1))
		}
	}
}

func (w *jtagDPWire) update() {
	offset := 0
	for i := range w.taps {
		tap := &w.taps[i]
		bits := 1
		switch {
		case w.state == jtag.UpdateIR:
			bits = tap.ir
		case tap.instruction&0xf == 0xe:
			bits = 32
		case tap.dp != nil && (tap.instruction&0xf == 0xa || tap.instruction&0xf == 0xb || tap.instruction&0xf == 8):
			bits = 35
		}
		var value uint64
		for bit := range bits {
			value |= uint64(w.shift[offset+bit]) << uint(bit)
		}
		if w.state == jtag.UpdateIR {
			tap.instruction = value
		} else if bits == 35 {
			tap.dp.update(tap.instruction, value)
		}
		offset += bits
	}
}

func jtagModelChain(t testing.TB, ir, position int) (*jtagDPModel, *jtagDPWire, *jtag.Chain) {
	t.Helper()
	m := &jtagDPModel{}
	w := &jtagDPWire{state: jtag.Reset, limit: 504, taps: []dpModelTAP{{ir: 12, id: 0x14730093}, {ir: 12, id: 0x14730093}}}
	w.taps[position] = dpModelTAP{ir: ir, id: 0x5ba00477, dp: m}
	layout := make(jtag.Layout, len(w.taps))
	for i, tap := range w.taps {
		var err error
		layout[i], err = jtag.IDCODE(tap.ir, tap.id)
		if err != nil {
			t.Fatal(err)
		}
	}
	chain, err := jtag.NewChain(jtag.New(w), layout)
	if err != nil {
		t.Fatal(err)
	}
	return m, w, chain
}
