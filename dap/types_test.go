package dap

import "testing"

func TestDecodeDPIDR(t *testing.T) {
	info, err := DecodeDPIDR(0x6ba02477)
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 6 || info.Part != 0xba || info.Version != 2 {
		t.Fatalf("DecodeDPIDR() = %+v", info)
	}
	if info.Minimal {
		t.Fatal("DecodeDPIDR() reported a minimal debug port")
	}
	if info.Designer != 0x23b {
		t.Fatalf("Designer = %#03x, want 0x23b", info.Designer)
	}
}

func TestDecodeDPIDRAcceptsUnrecognizedVersion(t *testing.T) {
	value := uint32(0x0f001001)
	info, err := DecodeDPIDR(value)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != 1 {
		t.Fatalf("Version = %d, want 1", info.Version)
	}
}

func TestDecodeDPIDRRejectsInvalidConstantBit(t *testing.T) {
	if _, err := DecodeDPIDR(0x2ba01476); err == nil {
		t.Fatal("DecodeDPIDR() succeeded without the constant-one bit")
	}
}

func TestDebugPortRegisterDescriptions(t *testing.T) {
	tests := []struct {
		reg             DPRegister
		offset          uint8
		bank            uint8
		bankIndependent bool
		readable        bool
		writable        bool
	}{
		{reg: DPIDR, offset: 0x00, bankIndependent: true, readable: true},
		{reg: ABORT, offset: 0x00, bankIndependent: true, writable: true},
		{reg: CTRLSTAT, offset: 0x04, readable: true, writable: true},
		{reg: DLCR, offset: 0x04, bank: 1, readable: true, writable: true},
		{reg: TARGETID, offset: 0x04, bank: 2, readable: true},
		{reg: DLPIDR, offset: 0x04, bank: 3, readable: true},
		{reg: EVENTSTAT, offset: 0x04, bank: 4, readable: true},
		{reg: SELECT, offset: 0x08, bankIndependent: true, writable: true},
		{reg: RESEND, offset: 0x08, bankIndependent: true, readable: true},
		{reg: RDBUFF, offset: 0x0c, bankIndependent: true, readable: true},
	}
	for _, test := range tests {
		info, ok := describeDPRegister(test.reg)
		if !ok {
			t.Fatalf("describeDPRegister(%v) failed", test.reg)
		}
		if info.offset != test.offset || info.bank != test.bank || info.bankIndependent != test.bankIndependent || info.readable != test.readable || info.writable != test.writable {
			t.Errorf("describeDPRegister(%v) = %+v", test.reg, info)
		}
		if test.reg.String() != info.name {
			t.Errorf("%v.String() = %q, want %q", test.reg, test.reg.String(), info.name)
		}
	}
	if _, ok := describeDPRegister(0); ok {
		t.Fatal("zero DPRegister has a description")
	}
}
