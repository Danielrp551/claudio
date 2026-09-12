// Package mcpserver exposes the workspace to Claude over MCP.
//
// It has two jobs. The first is to let Claude see the roster and promote a
// remote session to a native peer when it notices repeated traffic with one that
// has none. The second is to be the fallback transport: when the compatibility
// boundary cannot vouch for the environment, everything routes through here
// instead of through native peers, which is what keeps a change in Claude Code
// from killing the tool. See ADR-0008.
//
// That second job is why this is worth building well rather than treating as a
// secondary surface.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/danielrp551/claudio/internal/daemon"
)

// defaultProtocolVersion is used when a client does not name one.
const defaultProtocolVersion = "2024-11-05"

// Server speaks MCP over a pair of streams.
type Server struct {
	in      io.Reader
	out     io.Writer
	log     *slog.Logger
	control Control
	version string

	writeMu sync.Mutex
}

// Control is what this server needs from a running connector. It is declared
// here, where it is consumed.
type Control interface {
	Call(ctx context.Context, req daemon.ControlRequest) (daemon.ControlResponse, error)
}

// New builds a server.
func New(in io.Reader, out io.Writer, control Control, version string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{in: in, out: out, log: log, control: control, version: version}
}

// Serve reads requests until the input ends.
func (s *Server) Serve(ctx context.Context) error {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.reply(response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "that request could not be parsed"},
			})
			continue
		}

		resp, notification := s.handle(ctx, req)
		if notification {
			continue
		}
		s.reply(resp)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcpserver: reading requests: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req request) (response, bool) {
	resp := response{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = defaultProtocolVersion
		}
		resp.Result = mustJSON(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "claudio",
				"version": s.version,
			},
		})
		return resp, false

	case "notifications/initialized", "notifications/cancelled":
		return response{}, true

	case "ping":
		resp.Result = mustJSON(map[string]any{})
		return resp, false

	case "tools/list":
		resp.Result = mustJSON(map[string]any{"tools": toolDefinitions()})
		return resp, false

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "the arguments could not be read"}
			return resp, false
		}
		text, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			// A tool that failed reports through the result with isError rather
			// than as a protocol error, so the model can read what went wrong and
			// decide what to do about it.
			resp.Result = mustJSON(toolResult(err.Error(), true))
			return resp, false
		}
		resp.Result = mustJSON(toolResult(text, false))
		return resp, false

	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("unknown method %q", req.Method)}
		return resp, false
	}
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "workspace_sessions":
		resp, err := s.control.Call(ctx, daemon.ControlRequest{Op: daemon.ControlStatus})
		if err != nil {
			return "", err
		}
		return renderSessions(resp.Status), nil

	case "workspace_status":
		resp, err := s.control.Call(ctx, daemon.ControlRequest{Op: daemon.ControlStatus})
		if err != nil {
			return "", err
		}
		return renderStatus(resp.Status), nil

	case "workspace_send":
		var args struct {
			To          string `json:"to"`
			Text        string `json:"text"`
			FromSession string `json:"from_session"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", errors.New("the arguments could not be read")
		}
		if args.To == "" || args.Text == "" {
			return "", errors.New("a send needs both to and text")
		}
		resp, err := s.control.Call(ctx, daemon.ControlRequest{
			Op:          daemon.ControlSend,
			To:          args.To,
			Text:        args.Text,
			FromSession: args.FromSession,
		})
		if err != nil {
			return "", err
		}
		// Deliberately precise. The transport accepted it. Nobody has read it.
		return fmt.Sprintf(
			"Handed to the transport for %s, message %s. That means it was accepted "+
				"for delivery, not that the other session has read it.", args.To, resp.MsgID), nil

	case "workspace_promote":
		var args struct {
			Session string `json:"session"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || args.Session == "" {
			return "", errors.New("promote needs a session")
		}
		if _, err := s.control.Call(ctx, daemon.ControlRequest{
			Op: daemon.ControlPromote, Session: args.Session,
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"%s will get a native peer of its own. It appears in your agent list within "+
				"a few seconds, and from then on you can message it by name.", args.Session), nil

	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func renderSessions(status *daemon.Status) string {
	if status == nil || len(status.Remote) == 0 {
		return "No remote sessions are reachable right now."
	}
	var b strings.Builder
	b.WriteString("Remote sessions in this workspace:\n\n")
	for _, s := range status.Remote {
		fmt.Fprintf(&b, "  %s  (%s, session %s, %s)\n", s.ID, s.Person, s.Session, s.Status)
	}
	b.WriteString("\nSend with workspace_send, using the identifier on the left.\n")

	if len(status.Peers) > 0 {
		b.WriteString("\nThese already have a native peer, so you can also reach them with ")
		b.WriteString("SendMessage by name:\n\n")
		for _, p := range status.Peers {
			if p.Ready {
				fmt.Fprintf(&b, "  %s\n", p.Name)
			}
		}
	}
	return b.String()
}

func renderStatus(status *daemon.Status) string {
	if status == nil {
		return "The connector is not running."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Workspace: %s\n", status.Workspace)
	fmt.Fprintf(&b, "Platform: %s", status.Platform)
	if !status.Verified {
		b.WriteString(" (the protocol is not verified on this platform)")
	}
	b.WriteString("\n")
	if status.Degraded != "" {
		fmt.Fprintf(&b, "Degraded: %s\n", status.Degraded)
	}
	fmt.Fprintf(&b, "Promotion: %s, up to %d native peers\n",
		status.Policy.Mode, status.Policy.MaxGhosts)
	fmt.Fprintf(&b, "Local sessions: %d\n", len(status.Local))
	fmt.Fprintf(&b, "Remote sessions: %d\n", len(status.Remote))
	fmt.Fprintf(&b, "Native peers running: %d\n", len(status.Peers))
	return b.String()
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name": "workspace_sessions",
			"description": "List the Claude Code sessions of other people in this workspace that " +
				"this machine can reach. Use it before workspace_send, and to find out whether a " +
				"session already has a native peer you can message by name instead.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name": "workspace_send",
			"description": "Send a message to a Claude Code session belonging to another person in " +
				"this workspace. Prefer SendMessage when the session already appears in your agent " +
				"list, because that path is native. Use this when it does not.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{
						"type":        "string",
						"description": "The session identifier from workspace_sessions.",
					},
					"text": map[string]any{
						"type":        "string",
						"description": "The message. Plain text only, and never your conversation history.",
					},
					"from_session": map[string]any{
						"type":        "string",
						"description": "The name of this session, so the recipient knows who wrote.",
					},
				},
				"required": []string{"to", "text"},
			},
		},
		{
			"name": "workspace_promote",
			"description": "Give a remote session a native peer of its own, so it appears in your " +
				"agent list and you can message it with SendMessage. Worth doing when you find " +
				"yourself writing to the same session repeatedly. Each one costs a small process on " +
				"this machine, and the user sets how many are allowed.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session": map[string]any{
						"type":        "string",
						"description": "The session identifier from workspace_sessions.",
					},
				},
				"required": []string{"session"},
			},
		},
		{
			"name": "workspace_status",
			"description": "Report what the connector on this machine is doing: which workspace, " +
				"how many sessions it sees, how many native peers it runs, and whether it is running " +
				"degraded because the platform is not verified.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) reply(resp response) {
	blob, err := json.Marshal(resp)
	if err != nil {
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(append(blob, '\n'))
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mustJSON(v any) json.RawMessage {
	blob, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return blob
}
