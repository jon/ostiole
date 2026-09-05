package discover_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConsumerImports(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   []string
		absent []string
	}{
		{"core", nil, []string{"usb", "ftdi", "jlink", "cmsisdap"}},
		{"jlink", []string{"usb", "jlink", "jlink/discovery"}, []string{"ftdi", "cmsisdap"}},
		{"all", []string{"usb/discovery", "ftdi/discovery", "jlink/discovery", "cmsisdap/discovery"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./testdata/consumers/"+tc.name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("consumer dependencies: %v\n%s", err, out)
			}
			deps := "\n" + string(out)
			for _, name := range tc.want {
				if !strings.Contains(deps, "\ngithub.com/jon/ostiole/"+name+"\n") {
					t.Errorf("missing %s", name)
				}
			}
			for _, name := range tc.absent {
				if strings.Contains(deps, "\ngithub.com/jon/ostiole/"+name+"\n") {
					t.Errorf("unexpected %s", name)
				}
			}
		})
	}
}
