package main

import (
	"context"
	"os"

	"github.com/jon/ostiole/cmd/ost/internal/app"
)

func main() {
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
