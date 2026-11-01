package main

import (
	"strings"
	"testing"
)

func TestAPBaseErrorNamesItsFlag(t *testing.T) {
	for _, value := range []string{"not-an-address", "0x2001"} {
		_, err := selectAP(-1, value, false)
		if err == nil || !strings.Contains(err.Error(), "-ap-base") {
			t.Fatalf("AP base %q error=%v", value, err)
		}
	}
}
