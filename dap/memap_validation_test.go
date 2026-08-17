package dap

import (
	"math"
	"testing"
)

func TestValidateScalarAccess(t *testing.T) {
	tests := []struct {
		name         string
		addr         uint64
		size         TransferSize
		largeAddress bool
		wantErr      bool
	}{
		{name: "byte", addr: 3, size: Size8},
		{name: "halfword", addr: 2, size: Size16},
		{name: "word", addr: 4, size: Size32},
		{name: "doubleword", addr: 8, size: Size64},
		{name: "zero size", size: 0, wantErr: true},
		{name: "unaligned halfword", addr: 1, size: Size16, wantErr: true},
		{name: "unaligned word", addr: 2, size: Size32, wantErr: true},
		{name: "unaligned doubleword", addr: 4, size: Size64, wantErr: true},
		{name: "32-bit overflow", addr: math.MaxUint32, size: Size16, wantErr: true},
		{name: "64-bit overflow", addr: math.MaxUint64 - 3, size: Size64, largeAddress: true, wantErr: true},
		{name: "large address", addr: 1 << 32, size: Size32, largeAddress: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateScalarAccess(test.addr, test.size, test.largeAddress)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateScalarAccess() error = %v, want error=%t", err, test.wantErr)
			}
		})
	}
}
