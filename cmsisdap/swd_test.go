package cmsisdap

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jon/ostiole/swd"
)

var _ swd.Wire = (*Session)(nil)
var _ swd.TransferLimits = (*Session)(nil)

type swdSequencePeer struct {
	*swdCommandPeer
	sequenceCommands      [][]byte
	sequenceStatusAt      int
	sequenceUnimplemented bool
	wrongSequenceCommand  bool
	shortSequenceResponse bool
	direction, output     []bool
	cycles                int
}

func newSWDSequencePeer(packetSize uint16) *swdSequencePeer {
	peer := &swdSequencePeer{swdCommandPeer: newSWDCommandPeer(packetSize)}
	peer.device.claim.handle = peer.handle
	return peer
}

func (p *swdSequencePeer) handle(request []byte) ([]byte, error) {
	if len(request) == 0 || request[0] != commandSWDSequence {
		return p.swdCommandPeer.handle(request)
	}
	p.commands = append(p.commands, append([]byte(nil), request...))
	return p.handleSWDSequence(request)
}

func (p *swdSequencePeer) handleSWDSequence(request []byte) ([]byte, error) {
	p.sequenceCommands = append(p.sequenceCommands, append([]byte(nil), request...))
	if p.sequenceUnimplemented {
		p.sequenceUnimplemented = false
		return []byte{statusError}, nil
	}
	if p.sequenceStatusAt != 0 && len(p.sequenceCommands) == p.sequenceStatusAt {
		return []byte{commandSWDSequence, statusError}, nil
	}
	if len(request) < 3 || request[1] == 0 {
		return nil, errors.New("peer: malformed DAP_SWD_Sequence")
	}
	response := []byte{commandSWDSequence, statusOK}
	offset := 2
	for range int(request[1]) {
		captured, next, err := p.handleSWDSequenceRun(request, offset)
		if err != nil {
			return nil, err
		}
		response = append(response, captured...)
		offset = next
	}
	if offset != len(request) {
		return nil, errors.New("peer: trailing sequence data")
	}
	if p.wrongSequenceCommand {
		response[0] = commandSWJClock
		p.wrongSequenceCommand = false
	}
	if p.shortSequenceResponse && len(response) > 2 {
		response = response[:len(response)-1]
		p.shortSequenceResponse = false
	}
	return response, nil
}

func (p *swdSequencePeer) handleSWDSequenceRun(request []byte, offset int) ([]byte, int, error) {
	if offset >= len(request) || request[offset]&0x40 != 0 {
		return nil, offset, errors.New("peer: malformed sequence info")
	}
	info := request[offset]
	offset++
	bits := int(info & 0x3f)
	if bits == 0 {
		bits = 64
	}
	input := info&0x80 != 0
	bytes := (bits + 7) / 8
	if !input && offset+bytes > len(request) {
		return nil, offset, errors.New("peer: short sequence output")
	}
	captured := make([]byte, 0, bytes)
	if input {
		captured = make([]byte, bytes)
	}
	for bit := range bits {
		p.clockSWDBit(request, captured, offset, bit, input)
	}
	if !input {
		offset += bytes
	}
	return captured, offset, nil
}

func (p *swdSequencePeer) clockSWDBit(request, captured []byte, offset, bit int, input bool) {
	p.direction = append(p.direction, !input)
	value := false
	if !input {
		value = testBit(request[offset:], bit)
	}
	p.output = append(p.output, value)
	if input && p.cycles%5 == 2 {
		setTestBit(captured, bit, true)
	}
	p.cycles++
}

func TestPlanSWDPacketHonorsProtocolLimits(t *testing.T) {
	output := []byte{0xa5, 0x5a, 0xa5, 0x5a, 0xa5, 0x5a, 0xa5, 0x5a}
	request, captures, next := planSWDPacket([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, output, 64, 0, 64)
	want := append([]byte{commandSWDSequence, 1, 0}, output...)
	if !reflect.DeepEqual(request, want) || len(captures) != 0 || next != 64 {
		t.Fatalf("64-cycle output packet = (%x, %+v, %d), want (%x, none, 64)", request, captures, next, want)
	}

	request, captures, next = planSWDPacket(make([]byte, 8), make([]byte, 8), 64, 0, 64)
	if !reflect.DeepEqual(request, []byte{commandSWDSequence, 1, 0x80}) || !reflect.DeepEqual(captures, []swdCapture{{bits: 64}}) || next != 64 {
		t.Fatalf("64-cycle input packet = (%x, %+v, %d)", request, captures, next)
	}

	const bits = 255
	direction := make([]byte, (bits+7)/8)
	for bit := range bits {
		setTestBit(direction, bit, bit%2 == 0)
	}
	request, _, next = planSWDPacket(direction, make([]byte, len(direction)), bits, 0, 512)
	if request[1] != 255 || next != bits || len(request) > 512 {
		t.Fatalf("255-sequence packet = sequences %d next %d bytes %d", request[1], next, len(request))
	}
}

func TestMaxTransferBitsTracksSWDConfiguration(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	if got := session.MaxTransferBits(); got != 0 {
		t.Fatalf("unconfigured transfer limit = %d", got)
	}
	if err := session.ConfigureSWD(t.Context(), 100_000); err != nil {
		t.Fatal(err)
	}
	if got := session.MaxTransferBits(); got != 16_384 {
		t.Fatalf("configured transfer limit = %d", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := session.MaxTransferBits(); got != 0 {
		t.Fatalf("closed transfer limit = %d", got)
	}
}

func TestCloseAbandonsDisconnectAfterAmbiguousSequence(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	peer.device.claim.inputErr = errors.New("response failed")
	if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("SWDIO error = %v", err)
	}
	before := len(peer.effectCommands())
	if err := session.Close(); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("Close error = %v", err)
	}
	if got := len(peer.effectCommands()); got != before {
		t.Fatalf("Close sent a command on a poisoned session: %x", peer.effectCommands()[before:])
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 {
		t.Fatalf("close counts = claim %d device %d", peer.device.claim.closes, peer.device.closes)
	}
}

func TestSWDIOPreservesDirectionAndSamplesAcrossPackets(t *testing.T) {
	peer := newSWDSequencePeer(16)
	session, err := openSession(t.Context(), peer.device, WithSWD(4_000_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()

	const bits = 181
	direction := make([]byte, (bits+7)/8)
	output := make([]byte, len(direction))
	wantInput := make([]byte, len(direction))
	for bit := range bits {
		driven := bit/69%2 == 0 && bit%13 != 0
		setTestBit(direction, bit, driven)
		setTestBit(output, bit, bit%4 == 1)
		setTestBit(wantInput, bit, !driven && bit%5 == 2)
	}
	input, err := session.SWDIO(t.Context(), direction, output, bits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("SWDIO input = %x, want %x", input, wantInput)
	}
	if len(peer.sequenceCommands) < 2 {
		t.Fatalf("DAP_SWD_Sequence command count = %d, want packet splitting", len(peer.sequenceCommands))
	}
	for index, request := range peer.sequenceCommands {
		if len(request) > 16 {
			t.Fatalf("sequence command %d length = %d", index, len(request))
		}
	}
	if !reflect.DeepEqual(peer.direction, testBits(direction, bits)) {
		t.Fatal("probe observed different SWD direction bits")
	}
	wantOutput := make([]bool, bits)
	for bit := range bits {
		wantOutput[bit] = testBit(direction, bit) && testBit(output, bit)
	}
	if !reflect.DeepEqual(peer.output, wantOutput) {
		t.Fatal("probe observed output while the target owned SWDIO")
	}
}

func TestSWDIOValidatesBeforeTraffic(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	before := len(peer.commands)
	for _, test := range []struct {
		name              string
		direction, output []byte
		bits              int
	}{
		{name: "zero", bits: 0},
		{name: "negative", bits: -1},
		{name: "short direction", output: []byte{0}, bits: 1},
		{name: "short output", direction: []byte{0}, bits: 1},
		{name: "over limit", direction: make([]byte, 2049), output: make([]byte, 2049), bits: 16_385},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := session.SWDIO(t.Context(), test.direction, test.output, test.bits); err == nil {
				t.Fatal("SWDIO succeeded")
			}
		})
	}
	if len(peer.commands) != before {
		t.Fatalf("invalid SWDIO sent %d commands", len(peer.commands)-before)
	}
}

func TestSWDIOStopsAfterCompletePacketFailure(t *testing.T) {
	peer := newSWDSequencePeer(16)
	peer.sequenceStatusAt = 2
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	const bits = 181
	direction := make([]byte, (bits+7)/8)
	for bit := range bits {
		setTestBit(direction, bit, bit%2 == 0)
	}
	input, err := session.SWDIO(t.Context(), direction, make([]byte, len(direction)), bits)
	if input != nil || err == nil || !strings.Contains(err.Error(), "packet 2") {
		t.Fatalf("SWDIO = (%x, %v)", input, err)
	}
	if len(peer.sequenceCommands) != 2 || session.MaxClockHz() == 0 || session.poisoned {
		t.Fatalf("failure state = commands %d clock %d poisoned %t", len(peer.sequenceCommands), session.MaxClockHz(), session.poisoned)
	}
}

func TestSWDIOMalformedResponsePoisonsSession(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*swdSequencePeer)
	}{
		{name: "wrong command", configure: func(peer *swdSequencePeer) { peer.wrongSequenceCommand = true }},
		{name: "short capture", configure: func(peer *swdSequencePeer) { peer.shortSequenceResponse = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newSWDSequencePeer(64)
			test.configure(peer)
			session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("SWDIO error = %v", err)
			}
			before := len(peer.commands)
			if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("second SWDIO error = %v", err)
			}
			if len(peer.commands) != before {
				t.Fatal("poisoned session sent another command")
			}
			_ = session.Close()
		})
	}
}

func TestSWDIOCommandNotImplementedDoesNotPoisonSession(t *testing.T) {
	peer := newSWDSequencePeer(64)
	peer.sequenceUnimplemented = true
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil || errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("unimplemented SWDIO error = %v", err)
	}
	if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err != nil {
		t.Fatalf("SWDIO after defined error: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func setTestBit(data []byte, bit int, value bool) {
	if value {
		data[bit/8] |= 1 << uint(bit%8)
	}
}

func testBit(data []byte, bit int) bool { return data[bit/8]&(1<<uint(bit%8)) != 0 }

func testBits(data []byte, bits int) []bool {
	result := make([]bool, bits)
	for bit := range bits {
		result[bit] = testBit(data, bit)
	}
	return result
}
