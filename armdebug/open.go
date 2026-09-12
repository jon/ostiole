package armdebug

import (
	"context"

	"github.com/jon/ostiole/discover"
)

// Open discovers and opens exactly one selected probe, then connects its Arm
// debug port. Applications opt into providers through registration imports.
// Invalid port configuration or cleanup timeouts are rejected before discovery.
// Any discovery error prevents opening.
// DAP options are validated after activation by DAP. There is no
// fallback. Retain any non-nil returned owner for cleanup, even with an error.
func Open(ctx context.Context, selection discover.Selection, config Config) (*Conn, error) {
	if err := config.validate(ctx); err != nil {
		return nil, err
	}
	opened, err := discover.OpenProbe(ctx, selection)
	if err != nil {
		if opened == nil {
			return nil, err
		}
		return (&Conn{probe: opened, info: opened.Info()}).fail(err)
	}
	return Connect(ctx, opened, config)
}
