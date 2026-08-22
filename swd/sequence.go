// Package swd implements Serial Wire Debug framing over direction-explicit
// wire access.
//
// Conn enters SWD, establishes the response grammar, and exposes separate DP
// and AP reads and writes. Connect keeps inherited ORUNDETECT or tries to
// enable it, and uses overrun framing only if the bit reads back as set. Release
// restores the inherited setting. Register operations validate the physical
// address and make one attempt without retrying.
package swd

type sequence struct {
	direction []byte
	output    []byte
	bits      int
}

func (s *sequence) append(driven, value bool) {
	if s.bits%8 == 0 {
		s.direction = append(s.direction, 0)
		s.output = append(s.output, 0)
	}
	setBit(s.direction, s.bits, driven)
	setBit(s.output, s.bits, value)
	s.bits++
}

func (s *sequence) appendN(bits int, driven, value bool) {
	for range bits {
		s.append(driven, value)
	}
}

func (s *sequence) appendByte(driven bool, value byte) {
	for bit := range 8 {
		s.append(driven, value>>uint(bit)&1 != 0)
	}
}

func setBit(buf []byte, bit int, value bool) {
	if value {
		buf[bit/8] |= 1 << (uint(bit) % 8)
	}
}
