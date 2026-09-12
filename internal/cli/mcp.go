package cli

import (
	"context"
	"path/filepath"

	"github.com/danielrp551/claudio/internal/ccpeer"
	"github.com/danielrp551/claudio/internal/config"
	"github.com/danielrp551/claudio/internal/daemon"
	"github.com/danielrp551/claudio/internal/mcpserver"
)

// runMCP serves the MCP interface over standard input and output.
//
// Claude Code starts this process and speaks to it on those streams, so nothing
// but the protocol may be written to standard output. Every log line goes to
// standard error.
func runMCP(ctx context.Context, env Env, args []string) error {
	fs := flagSet("mcp", env.Stderr)
	level := fs.String("log-level", "warn", "debug, info, warn, or error")
	if err := parse(fs, args); err != nil {
		return err
	}

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	control, err := daemon.DialControl(
		filepath.Join(dir, daemon.AddressFile), ccpeer.Platform())
	if err != nil {
		return err
	}

	log := newLogger(env.Stderr, *level)
	return mcpserver.New(env.Stdin, env.Stdout, control, env.Version, log).Serve(ctx)
}
