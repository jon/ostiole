package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

type controlTarget struct {
	inherited             bool
	halts, steps, resumes int
	reads                 []cortexm.Register
}

func (c *controlTarget) Identity() cortexm.Identity           { return cortexm.Identity{Raw: 0x410cc200} }
func (c *controlTarget) Halted(context.Context) (bool, error) { return c.inherited, nil }
func (c *controlTarget) Halt(context.Context) error {
	c.halts++
	return nil
}
func (c *controlTarget) Step(context.Context) error {
	c.steps++
	return nil
}
func (c *controlTarget) Resume(context.Context) error {
	c.resumes++
	return nil
}

func (c *controlTarget) ReadRegister(_ context.Context, reg cortexm.Register) (uint32, error) {
	c.reads = append(c.reads, reg)
	return 0x2000003c, nil
}

func TestControlPreservesInheritedHalt(t *testing.T) {
	for _, step := range []bool{false, true} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			core := &controlTarget{inherited: true}
			output, err := captureControl(t, core, step)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "already halted; preserving halt") || !strings.Contains(output, "PC=0x2000003c") || strings.Contains(output, "resumed") || strings.Contains(output, "stepped;") {
				t.Fatalf("inherited halt output: %s", output)
			}
			if core.steps != 0 || core.resumes != 0 || core.halts != 1 {
				t.Fatalf("inherited halt control: %+v", core)
			}
			checkRegisterReads(t, core.reads)
		})
	}
}

func TestControlResumesItsRequestedHalt(t *testing.T) {
	for _, step := range []bool{false, true} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			core := &controlTarget{}
			output, err := captureControl(t, core, step)
			if err != nil || !strings.Contains(output, "resumed") || core.halts != 1 || core.resumes != 1 {
				t.Fatalf("control=%+v output=%s error=%v", core, output, err)
			}
			if step {
				if core.steps != 1 || !strings.Contains(output, "stepped; PC=0x2000003c") || len(core.reads) != 5 || core.reads[4] != cortexm.PC {
					t.Fatalf("step=%+v output=%s", core, output)
				}
			} else if core.steps != 0 || len(core.reads) != 4 {
				t.Fatalf("unrequested step=%+v", core)
			}
			checkRegisterReads(t, core.reads[:4])
		})
	}
}

func checkRegisterReads(t *testing.T, got []cortexm.Register) {
	t.Helper()
	want := []cortexm.Register{cortexm.PC, cortexm.SP, cortexm.R0, cortexm.R4}
	if len(got) != len(want) {
		t.Fatalf("register reads=%v", got)
	}
	for i, reg := range want {
		if got[i] != reg {
			t.Fatalf("register reads=%v", got)
		}
	}
}

func captureControl(t *testing.T, core controlledTarget, step bool) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	err = control(t.Context(), core, step)
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return string(output), err
}
