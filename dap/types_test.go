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

func TestDebugPortRegisterAddresses(t *testing.T) {
	tests := []struct {
		reg  DPReg
		want uint8
	}{
		{ABORT, 0x00},
		{DPIDR, 0x00},
		{CTRLSTAT, 0x04},
		{SELECT, 0x08},
		{RDBUFF, 0x0c},
	}
	for _, test := range tests {
		if uint8(test.reg) != test.want {
			t.Errorf("register address = %#02x, want %#02x", test.reg, test.want)
		}
	}
}
