// Command roster runs the Roster control plane, worker and CLI.
package main

import (
	"context"
	"os"

	"github.com/wojtekmanke/roster/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(context.Background(), cli.Env{
		Args:    os.Args[1:],
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
	}))
}
