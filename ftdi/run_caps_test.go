package ftdi

import "testing"

func TestSWDRunCapsPreserveDirectionAndOutput(t *testing.T) {
	const bits = 50
	direction, output := make([]byte, 7), make([]byte, 7)
	for i := range bits {
		if i/9%2 == 0 {
			direction[i/8] |= 1 << uint(i%8)
		}
		if i%3 != 0 {
			output[i/8] |= 1 << uint(i%8)
		}
	}
	commands, _ := swdCommands(direction, output, bits)
	clock := 0
	for at := 0; at < len(commands); {
		op := commands[at]
		at++
		switch op {
		case 0x80:
			at += 2
		case 0x87:
		case 0x1b, 0x2a:
			count := int(commands[at]) + 1
			at++
			if count > 8 {
				t.Fatalf("oversized SWD run: %d", count)
			}
			data := byte(0)
			if op == 0x1b {
				data = commands[at]
				at++
			}
			for j := range count {
				assertSWDRunClock(t, direction, output, bits, clock, data, j, op)
				clock++
			}

		default:
			t.Fatalf("unexpected command %#x", op)
		}
	}
	if clock != bits {
		t.Fatalf("clock count %d, want %d", clock, bits)
	}
}

func TestLongTMSRunKeepsSevenBitCommands(t *testing.T) {
	commands, _ := jtagCommands([]byte{0xff, 0xff, 0xff}, []byte{0, 0, 0}, 24)
	ms, di, _ := interpretJTAG(t, commands)
	if len(ms) != 24 {
		t.Fatalf("clock count %d", len(ms))
	}
	for i := range ms {
		if !ms[i] || di[i] {
			t.Fatalf("wrong pins at clock %d", i)
		}
	}
}

func assertSWDRunClock(t *testing.T, direction, output []byte, bits, clock int, data byte, index int, op byte) {
	t.Helper()
	if clock >= bits {
		t.Fatal("too many clocks")
	}
	host := direction[clock/8]>>uint(clock%8)&1 != 0
	if host != (op == 0x1b) {
		t.Fatalf("wrong direction at clock %d", clock)
	}
	if host && data>>uint(index)&1 != output[clock/8]>>uint(clock%8)&1 {
		t.Fatalf("wrong output at clock %d", clock)
	}
}
