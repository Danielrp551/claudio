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
  expose      share a local session with the workspace
  unexpose    stop sharing a local session
  sessions    list sessions reachable in a workspace
  trust       set the trust level for a person, workspace, or session
  policy      set promotion mode and the cap on native peers
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
	case "daemon":
		return runDaemon(ctx, env, rest)
	case "relay":
		return runRelay(ctx, env, rest)
	case "workspace":
		return runWorkspace(ctx, env, rest)
	case "invite":
		return runInvite(ctx, env, rest)
	case "join":
		return runJoin(ctx, env, rest)
	case "expose":
		return runExpose(ctx, env, rest)
	case "unexpose":
		return runUnexpose(ctx, env, rest)
	case "sessions":
		return runSessions(ctx, env, rest)
	case "trust":
		return runTrust(ctx, env, rest)
	case "policy":
		return runPolicy(ctx, env, rest)
	case "status":
		return runStatus(ctx, env, rest)
	case "doctor":
		return runDoctor(ctx, env, rest)
	case "mcp":
		return runMCP(ctx, env, rest)
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

// parse accepts flags before or after the positional arguments.
//
// The standard library stops parsing at the first argument that is not a flag,
// so "workspace create acme --name Acme" would treat the name as another
// positional and fail. People write it that way constantly, and a tool that
// answers such a line with a usage error is a tool that feels broken. This
// reorders the arguments first, which is what most command line tools do.
func parse(fs *flag.FlagSet, args []string) error {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}

		flags = append(flags, a)

		// A flag written as --name=value carries its own value, and a boolean
		// flag never takes one. Anything else consumes the argument after it.
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if isBoolFlag(fs, name) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return fs.Parse(append(flags, positional...))
}

// isBoolFlag reports whether a flag takes no value.
func isBoolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
