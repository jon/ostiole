package cortexm_test

import (
	"context"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestIdentifyReadsAndDecodesCPUID(t *testing.T) {
	reader := &wordReader{value: 0x410fc241}
	info, err := cortexm.Identify(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.address != 0xe000ed00 {
		t.Fatalf("CPUID address = %#08x", reader.address)
	}
	if info.Raw != 0x410fc241 || info.Implementer != 0x41 ||
		info.Variant != 0 || info.Architecture != 0x0f ||
		info.Part != 0x0c24 || info.Revision != 1 {
		t.Fatalf("Identity = %#v", info)
	}
}

func TestIdentifyRejectsImplausibleCPUID(t *testing.T) {
	for _, value := range []uint32{0, 0x420fc241, 0x410f0001} {
		reader := &wordReader{value: value}
		if _, err := cortexm.Identify(t.Context(), reader); err == nil {
			t.Fatalf("Identify() accepted CPUID %#08x", value)
		}
	}
}

func TestIdentifyRequiresWordReader(t *testing.T) {
	if _, err := cortexm.Identify(t.Context(), nil); err == nil {
		t.Fatal("Identify() succeeded without a word reader")
	}
}

type wordReader struct {
	address uint32
	value   uint32
}

func (r *wordReader) ReadWord(_ context.Context, address uint32) (uint32, error) {
	r.address = address
	return r.value, nil
}
