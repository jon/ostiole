package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/usb"
)

func TestRunShowsHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		if status := Run(t.Context(), args, &stdout, &stderr); status != 0 {
			t.Fatalf("Run(%q) status = %d", args, status)
		}
		want := "Usage:\n  ost ftdi list\n  ost swd dpidr\n" +
			"  ost dap dp id\n  ost dap ap id [--ap N]\n" +
			"  ost target cortex-m id [--ap N]\n  ost help\n"
		if got := stdout.String(); got != want {
			t.Fatalf("Run(%q) stdout = %q, want %q", args, got, want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := Run(t.Context(), []string{"unknown"}, &stdout, &stderr); status != 2 {
		t.Fatalf("Run() status = %d, want 2", status)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	want := "ost: unknown command \"unknown\"\n\n" +
		"Usage:\n  ost ftdi list\n  ost swd dpidr\n" +
		"  ost dap dp id\n  ost dap ap id [--ap N]\n" +
		"  ost target cortex-m id [--ap N]\n  ost help\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRunIdentifiesSelectedCortexMTarget(t *testing.T) {
	info, err := dap.DecodeDPIDR(0x2ba01477)
	if err != nil {
		t.Fatal(err)
	}
	var selected dap.APSel
	var stdout bytes.Buffer
	ops := operations{
		identifyCortexM: func(_ context.Context, selection dap.APSel) (targetIdentity, error) {
			selected = selection
			return targetIdentity{
				dpidr:     info,
				selection: selection,
				apidr:     0x24770011,
				processor: cortexm.Identity{Raw: 0x410fc241},
			}, nil
		},
	}
	args := []string{"target", "cortex-m", "id", "--ap", "3"}
	err = run(t.Context(), args, &stdout, ops)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 3 {
		t.Fatalf("selected AP = %d, want 3", selected)
	}
	want := "DPIDR=0x2ba01477 AP3_IDR=0x24770011 CPUID=0x410fc241\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunInspectsSelectedAccessPort(t *testing.T) {
	info, err := dap.DecodeDPIDR(0x2ba01477)
	if err != nil {
		t.Fatal(err)
	}
	var selected dap.APSel
	var stdout bytes.Buffer
	ops := operations{
		inspectAP: func(_ context.Context, selection dap.APSel) (apIdentity, error) {
			selected = selection
			return apIdentity{dpidr: info, selection: selection, idr: 0x24770011}, nil
		},
	}
	err = run(t.Context(), []string{"dap", "ap", "id", "--ap", "3"}, &stdout, ops)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 3 {
		t.Fatalf("selected AP = %d, want 3", selected)
	}
	want := "DPIDR=0x2ba01477 AP3_IDR=0x24770011\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunRejectsOutOfRangeAccessPort(t *testing.T) {
	err := run(t.Context(), []string{"dap", "ap", "id", "--ap", "256"},
		&bytes.Buffer{}, operations{})
	var commandErr *usageError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %v, want usage error", err)
	}
}

func TestRunInspectsDebugPort(t *testing.T) {
	info, err := dap.DecodeDPIDR(0x2ba01477)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	ops := operations{
		inspectDP: func(context.Context) (dap.DPIDRInfo, error) {
			return info, nil
		},
	}
	err = run(t.Context(), []string{"dap", "dp", "id"}, &stdout, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := "DPIDR=0x2ba01477 REVISION=2 PART=0xba " +
		"MINIMAL=false VERSION=1 DESIGNER=0x23b\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunReadsSWDIdentity(t *testing.T) {
	var stdout bytes.Buffer
	ops := operations{
		readDPIDR: func(context.Context) (uint32, error) {
			return 0x2ba01477, nil
		},
	}
	err := run(t.Context(), []string{"swd", "dpidr"}, &stdout, ops)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "DPIDR=0x2ba01477\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunListsFTDIAttachments(t *testing.T) {
	var stdout bytes.Buffer
	ops := operations{
		listFTDI: func(context.Context) ([]usb.DeviceInfo, error) {
			return []usb.DeviceInfo{
				{VID: 0x0403, PID: 0x6010, Bus: 1, Address: 2},
				{VID: 0x0403, PID: 0x6014, Bus: 3, Address: 4},
			}, nil
		},
	}
	err := run(t.Context(), []string{"ftdi", "list"}, &stdout, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := "001:002 0403:6010\n003:004 0403:6014\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}
