package armdebug_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCoreConsumerImportsNoDriver(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "./testdata/core").CombinedOutput()
	if err != nil {
		t.Fatalf("consumer dependencies: %v\n%s", err, out)
	}
	for _, name := range []string{"usb", "ftdi", "jlink", "cmsisdap", "discover/probes"} {
		if strings.Contains("\n"+string(out), "\ngithub.com/jon/ostiole/"+name+"\n") {
			t.Errorf("unexpected dependency %s", name)
		}
	}
	if out, err := exec.CommandContext(t.Context(), "go", "test", "./testdata/core").CombinedOutput(); err != nil {
		t.Fatalf("consumer build: %v\n%s", err, out)
	}
}
