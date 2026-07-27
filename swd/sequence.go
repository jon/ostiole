// Package swd implements Serial Wire Debug framing over direction-explicit
// wire access.
package swd

// Sequence packs direction and output values for an SWD wire operation.
type Sequence struct {
	direction []byte
	output    []byte
	bits      int
}

// Append adds one bit to the sequence.
func (s *Sequence) Append(driven, value bool) {
	if s.bits%8 == 0 {
		s.direction = append(s.direction, 0)
		s.output = append(s.output, 0)
	}
	setBit(s.direction, s.bits, driven)
	setBit(s.output, s.bits, value)
	s.bits++
}

// AppendN adds bits copies of one direction and value.
func (s *Sequence) AppendN(bits int, driven, value bool) {
	for range bits {
		s.Append(driven, value)
	}
}

// AppendByte adds one least-significant-bit-first byte.
func (s *Sequence) AppendByte(driven bool, value byte) {
	for bit := range 8 {
		s.Append(driven, value>>uint(bit)&1 != 0)
	}
}

// Direction returns the packed direction bits.
func (s *Sequence) Direction() []byte {
	return s.direction
}

// Output returns the packed output bits.
func (s *Sequence) Output() []byte {
	return s.output
}

// Bits returns the sequence length.
func (s *Sequence) Bits() int {
	return s.bits
}

func setBit(buf []byte, bit int, value bool) {
	if value {
		buf[bit/8] |= 1 << (uint(bit) % 8)
	}
}
