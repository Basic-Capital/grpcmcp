package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// writeJSONRPCError answers with an HTTP status and a JSON-RPC error whose id is
// null. JSON-RPC 2.0 requires the id member on every response; null is the value
// when the request id cannot be determined — for example when the body has not
// been parsed yet, as here on the Origin and protocol-version checks.
func writeJSONRPCError(w http.ResponseWriter, status int, code int, message string) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   map[string]any{"code": code, "message": message},
	})
	if err != nil {
		// The value is a fixed shape of strings, ints, and a nil id, so this
		// cannot fail. Answer with the status alone rather than a half-written body.
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// rejectBrowserOrigin answers 403 when a request carries an Origin header.
//
// MCP requires a server to validate Origin on every inbound connection, to stop
// a DNS rebinding attack: a page on a site the operator visits re-resolves its
// own hostname to this server's address and then reads the response, because
// the browser treats the reply as same-origin.
//
// Only a browser sets Origin. An MCP client speaks HTTP directly and sets none,
// so no legitimate caller of this server sends one. That makes the whole valid
// set empty and turns the check into a plain rejection, with no origin list to
// configure and keep correct. Add an allowlist when a browser client exists.
func rejectBrowserOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, found := r.Header[http.CanonicalHeaderKey("Origin")]; found {
			writeJSONRPCError(w, http.StatusForbidden, -32600, "Origin not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rejectUnsupportedProtocolVersion answers 400 when a request names a protocol
// version this server does not implement.
//
// MCP requires the rejection so a client learns at once that the two sides do
// not agree, and can retry with a version both support. mcp-go does not do it:
// its negotiation reads the version only while handling initialize, and answers
// an unknown one with the newest version it knows rather than an error, which
// tells the client it agreed to something it never checked.
//
// A request with no header is allowed. The spec lets a server that serves
// clients older than 2025-06-18, which did not send the header, read a missing
// header as 2025-03-26, and mcp-go does exactly that.
func rejectUnsupportedProtocolVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("Mcp-Protocol-Version")
		if version != "" && !slices.Contains(mcp.ValidProtocolVersions, version) {
			writeJSONRPCError(w, http.StatusBadRequest, -32600, fmt.Sprintf(
				"Unsupported protocol version %q: this server supports %s",
				version, strings.Join(mcp.ValidProtocolVersions, ", "),
			))
			return
		}
		next.ServeHTTP(w, r)
	})
}
