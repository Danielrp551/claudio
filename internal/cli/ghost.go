package cli

import (
	"context"
	"time"

	"github.com/Danielrp551/claudio/internal/ghost"
)

// runGhost holds one native peer endpoint.
//
// It is not a command a person runs. The daemon spawns it, drives it over
// standard input, and reads its events from standard output, so this function
// must never write anything to standard output that is not part of that
// protocol. Every log line goes to standard error.
func runGhost(ctx context.Context, env Env, args []string) error {
	fs := flagSet("ghost", env.Stderr)
	level := fs.String("log-level", "info", "debug, info, warn, or error")
	heartbeat := fs.Duration("heartbeat", ghost.DefaultHeartbeat,
		"how often to refresh the published record")

	if err := parse(fs, args); err != nil {
		return err
	}

	return ghost.Run(ctx, ghost.Config{
		In:        env.Stdin,
		Out:       env.Stdout,
		Logger:    newLogger(env.Stderr, *level),
		Heartbeat: durationOr(*heartbeat, ghost.DefaultHeartbeat),
	})
}

func durationOr(v, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}
