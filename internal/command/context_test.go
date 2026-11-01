package command_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jon/ostiole/internal/command"
)

func TestSignalContextCancelsBeforeProcessExit(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalContextChild$")
			child.Env = append(os.Environ(), "OSTIOLE_TEST_SIGNAL_CHILD=1")
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					cancel()
					_ = child.Wait()
				}
			})
			output := bufio.NewReader(stdout)
			ready, err := output.ReadString('\n')
			if err != nil || ready != "ready\n" {
				t.Fatalf("child ready=%q error=%v", ready, err)
			}
			if err := child.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			rest, readErr := io.ReadAll(output)
			if err := child.Wait(); err != nil || readErr != nil || !strings.Contains(string(rest), "operation canceled") {
				t.Fatalf("signal=%v exit=%v read=%v output=%s", signal, err, readErr, rest)
			}
		})
	}
}

func TestSignalContextChild(t *testing.T) {
	if os.Getenv("OSTIOLE_TEST_SIGNAL_CHILD") != "1" {
		return
	}
	ctx, stop := command.SignalContext()
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatal(ctx.Err())
	}
	fmt.Println("operation canceled")
}
