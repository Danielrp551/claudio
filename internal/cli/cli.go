package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Usage is printed for help and for an empty command line.
const Usage = `claudio - shared workspaces between Claude Code sessions

Usage:
  claudio <command> [flags]

Runtime commands:
  daemon      run the connector for this machine
  ghost       hold one native peer endpoint, spawned by the daemon
  mcp         serve the MCP interface over stdio
  relay       run a relay server

Workspace commands:
  workspace   create and inspect workspaces
  invite      create an invitation
  join        redeem an invitation
  sessions    list sessions reachable in a workspace
  promote     give a remote session its own native peer
  demote      release the process backing a native peer
  trust       set the trust level for a person, workspace, or session
  policy      set promotion mode and the ghost cap
  status      show what this machine is running and why
  doctor      report what this machine can and cannot do

Other:
  version     print the version
  help        print this message
`

// ErrUsage asks the caller to print the usage text and exit with code two.
var ErrUsage = errors.New("usage")

// ErrNotImplemented marks a command whose surface is defined but whose body is
// not written yet. Returning it keeps the command list honest, because a command
// either works or says plainly that it does not.
var ErrNotImplemented = errors.New("not implemented yet")

// Env carries everything a command needs from the outside world, so tests can
// supply their own.
type Env struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
}

// Run dispatches one command line.
func Run(ctx context.Context, env Env, args []string) error {
	if env.Stdin == nil {
		env.Stdin = os.Stdin
	}
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}

	if len(args) == 0 {
		return ErrUsage
	}

	name, rest := args[0], args[1:]

	switch name {
	case "version":
		fmt.Fprintln(env.Stdout, env.Version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, Usage)
		return nil
	case "ghost":
		return runGhost(ctx, env, rest)
	case "daemon", "mcp", "relay",
		"workspace", "invite", "join", "sessions",
		"promote", "demote", "trust", "policy", "status", "doctor":
		return fmt.Errorf("%s: %w", name, ErrNotImplemented)
	default:
		return fmt.Errorf("unknown command %q, run \"claudio help\"", name)
	}
}

// newLogger builds the logger a command uses. Everything goes to standard error,
// because standard output belongs to the protocol on the commands that speak one.
func newLogger(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// flagSet builds a flag set that writes its errors where the caller wants them
// and never calls os.Exit, so Run can report a usage problem like any other.
func flagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}
