package app

import (
	"bytes"
	"testing"
)

func TestRunShowsHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		if status := Run(t.Context(), args, &stdout, &stderr); status != 0 {
			t.Fatalf("Run(%q) status = %d", args, status)
		}
		if got, want := stdout.String(), "Usage:\n  ost help\n"; got != want {
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
	want := "ost: unknown command \"unknown\"\n\nUsage:\n  ost help\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}
