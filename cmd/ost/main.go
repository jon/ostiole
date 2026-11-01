package main

import (
	"os"

	"github.com/jon/ostiole/cmd/ost/internal/app"
	"github.com/jon/ostiole/internal/command"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := command.SignalContext()
	defer stop()
	return app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}
