// Package mcpserver exposes the workspace to Claude over MCP.
//
// It has two jobs. First, it lets Claude see the roster and promote a remote
// session to a native peer when it notices repeated traffic with a session that
// has no ghost. Second, it is the fallback transport: when the compatibility
// boundary cannot vouch for the environment, everything routes through here
// instead of through native peers. See ADR-0008.
//
// That second job is why this package is worth building well rather than
// treating as a secondary surface.
package mcpserver
