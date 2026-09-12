// Command claudio is the single entry point for every role this tool plays.
//
// The same executable runs as the connector, as a ghost child process, as an MCP
// server, as a relay, and as the human facing command line. See ADR-0002 for why
// there is one binary rather than four.
//
// This file stays thin on purpose: parse the arguments, build the environment,
// and hand over. Everything else lives in internal packages. See ADR-0009.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Danielrp551/claudio/internal/cli"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// os.Exit is called here and nowhere else, so every deferred call inside run
	// actually runs. Calling it from inside would skip the signal cleanup.
	os.Exit(run())
}

func run() int {
	// Every long running command honours interrupt and terminate, because a
	// ghost has to remove its record before it exits. See ADR-0004 for the
	// ordering that shutdown follows.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
	}

	err := cli.Run(ctx, env, os.Args[1:])
	switch {
	case err == nil:
		return 0
	case errors.Is(err, cli.ErrUsage):
		fmt.Fprint(os.Stderr, cli.Usage)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "claudio:", err)
		return 1
	}
}
