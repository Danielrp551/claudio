package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
	"github.com/Danielrp551/claudio/internal/config"
	"github.com/Danielrp551/claudio/internal/daemon"
)

// runPending lists the messages the hold gate is withholding.
//
// The gate is the part of the trust model that was described and never built.
// ADR-0006 says there is one, and until now nothing in the code did anything
// with it, so a person who set hold got silence rather than a question.
func runPending(ctx context.Context, env Env, args []string) error {
	fs := flagSet("pending", env.Stderr)
	if err := parse(fs, args); err != nil {
		return err
	}

	resp, err := callConnector(ctx, daemon.ControlRequest{Op: daemon.ControlPending})
	if err != nil {
		return err
	}

	if len(resp.Held) == 0 {
		fmt.Fprintln(env.Stdout, "nothing is waiting for your approval")
		return nil
	}

	fmt.Fprintf(env.Stdout, "%d message(s) waiting for your approval:\n\n", len(resp.Held))
	for _, h := range resp.Held {
		fmt.Fprintf(env.Stdout, "  %s  %s  from %s",
			h.ID, h.At.Local().Format("15:04:05"), h.Delivery.From.Person)
		if h.Delivery.From.Session != "" {
			fmt.Fprintf(env.Stdout, " (%s)", h.Delivery.From.Session)
		}
		fmt.Fprintf(env.Stdout, " to %s\n", h.Delivery.ToSession)
		fmt.Fprintf(env.Stdout, "      %s\n", firstLine(h.Delivery.Text, 90))
	}

	if resp.Dropped > 0 {
		fmt.Fprintf(env.Stdout,
			"\n%d older message(s) were dropped because the queue was full\n", resp.Dropped)
	}
	fmt.Fprintln(env.Stdout,
		"\n\"claudio approve <id>\" delivers one, \"claudio drop <id>\" discards it.")
	return nil
}

// runApprove delivers one withheld message.
func runApprove(ctx context.Context, env Env, args []string) error {
	return decide(ctx, env, args, daemon.ControlApprove, "approve",
		"delivered %s into the session it was addressed to\n")
}

// runDrop discards one withheld message.
func runDrop(ctx context.Context, env Env, args []string) error {
	return decide(ctx, env, args, daemon.ControlDrop, "drop",
		"discarded %s, and the sender is not told\n")
}

func decide(ctx context.Context, env Env, args []string, op, name, success string) error {
	fs := flagSet(name, env.Stderr)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: claudio %s <id>, from \"claudio pending\"", name)
	}

	if _, err := callConnector(ctx, daemon.ControlRequest{Op: op, ID: fs.Arg(0)}); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, success, fs.Arg(0))
	return nil
}

// firstLine renders enough of a message to recognise it without pasting a
// stranger's words, at length, into somebody's terminal.
func firstLine(text string, max int) string {
	for i, r := range text {
		if r == '\n' || r == '\r' {
			text = text[:i]
			break
		}
	}
	if len([]rune(text)) > max {
		return string([]rune(text)[:max-3]) + "..."
	}
	if text == "" {
		return "(no text)"
	}
	return text
}

// callConnector makes one control call to the running connector.
func callConnector(ctx context.Context, req daemon.ControlRequest) (daemon.ControlResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	dir, err := config.Dir()
	if err != nil {
		return daemon.ControlResponse{}, err
	}
	control, err := daemon.DialControl(
		filepath.Join(dir, daemon.AddressFile), ccpeer.Platform())
	if err != nil {
		return daemon.ControlResponse{}, err
	}

	resp, err := control.Call(ctx, req)
	if err != nil {
		return daemon.ControlResponse{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
