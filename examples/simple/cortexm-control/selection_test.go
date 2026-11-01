package main

import (
	"testing"

	"github.com/jon/ostiole/discover"
)

func TestControlSelectionPreservesExactFTDIFunctionAndLocation(t *testing.T) {
	for _, serial := range []string{"serial", ""} {
		selection, err := selectProbe("ftdi", serial, "B", "usb:1-2.3")
		if err != nil {
			t.Fatal(err)
		}
		want := discover.Selection{Provider: "ftdi", Serial: serial, Function: "B", Location: "usb:1-2.3"}
		if selection != want {
			t.Fatalf("selection=%+v want=%+v", selection, want)
		}
	}
}

func TestControlSelectionRequiresAnAttachmentIdentifier(t *testing.T) {
	for _, provider := range []string{"", "ftdi"} {
		if _, err := selectProbe(provider, "", "B", ""); err == nil {
			t.Fatal("accepted no exact attachment identifier")
		}
	}
	if _, err := selectProbe("", "serial", "", ""); err == nil {
		t.Fatal("accepted no provider")
	}
}
