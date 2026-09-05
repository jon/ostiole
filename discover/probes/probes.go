// Package probes enables every bundled probe provider through registration.
// Blank-import it in generic tools; import individual driver discovery packages
// instead when a binary should include only selected providers.
package probes

import (
	_ "github.com/jon/ostiole/cmsisdap/discovery"
	_ "github.com/jon/ostiole/ftdi/discovery"
	_ "github.com/jon/ostiole/jlink/discovery"
)
