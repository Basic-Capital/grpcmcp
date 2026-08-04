package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/protobuf/types/descriptorpb"
)

// newTestHTTPServer builds the same handler main serves: a stateless streamable
// HTTP server with no GET stream, behind the Origin check, mounted at /mcp.
func newTestHTTPServer(t *testing.T) http.Handler {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			buildEmptyFileDescriptor(),
			buildFileDescriptor("com.example.wallet", "WalletService", []string{"GetPlan"}),
		},
	}
	tools, err := grpcmcp.Tools(grpcmcp.Config{
		Descriptors: fds,
		BaseURL:     "http://localhost:0",
		ToolName:    buildToolNamer(fds, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	srv.AddTools(tools...)

	httpSrv := server.NewStreamableHTTPServer(srv,
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
	)
	mux := http.NewServeMux()
	mux.Handle(streamableEndpointPath, rejectBrowserOrigin(rejectUnsupportedProtocolVersion(httpSrv)))
	return mux
}

// post sends one JSON-RPC message to /mcp and returns the response.
func post(t *testing.T, h http.Handler, body string, header http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, streamableEndpointPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// assertJSONRPCErrorIDNull checks that an error response includes "id": null,
// which JSON-RPC 2.0 requires when the request id is unknown.
func assertJSONRPCErrorIDNull(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("decode %q: %v", string(b), err)
	}
	if string(msg.ID) != "null" {
		t.Errorf("id = %s, want null", msg.ID)
	}
	return b
}

// decodeResult reads the JSON-RPC result object out of an HTTP response.
func decodeResult(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var msg struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("decode %q: %v", string(b), err)
	}
	if msg.Error != nil {
		t.Fatalf("unexpected JSON-RPC error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	return msg.Result
}

// TestStatelessListToolsWithoutSession is the property the whole transport
// change rests on: a bare POST works with no prior initialize and no session
// header, so any replica can serve any request.
func TestStatelessListToolsWithoutSession(t *testing.T) {
	h := newTestHTTPServer(t)

	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	result := decodeResult(t, resp)
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 tool", result["tools"])
	}
	// The server must not mint a session for the client to carry forward.
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.Errorf("Mcp-Session-Id = %q, want no session header", id)
	}
}

// TestStatelessIgnoresSessionHeader covers the MCP 2026-07-28 rule for a server
// that no longer speaks the session-based revisions: ignore an inbound
// Mcp-Session-Id rather than reject it, and do not echo one back.
func TestStatelessIgnoresSessionHeader(t *testing.T) {
	h := newTestHTTPServer(t)

	header := http.Header{"Mcp-Session-Id": []string{"stale-session-from-a-dead-pod"}}
	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, header)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.Errorf("Mcp-Session-Id = %q, want no session header", id)
	}
}

// TestToolCallWithoutSession checks that a tool call needs no session either.
// The backend is unreachable, so the call reports a tool execution error, which
// still proves the request reached the handler and was dispatched.
func TestToolCallWithoutSession(t *testing.T) {
	h := newTestHTTPServer(t)

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"GetPlan","arguments":{}}}`
	resp := post(t, h, body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	result := decodeResult(t, resp)
	if isError, _ := result["isError"].(bool); !isError {
		t.Errorf("isError = %v, want true for an unreachable backend", result["isError"])
	}
}

// TestGetReturns405 covers the spec rule for a server that offers no SSE stream
// at its endpoint. It must answer GET with 405, not 200 and not 404.
func TestGetReturns405(t *testing.T) {
	h := newTestHTTPServer(t)

	req := httptest.NewRequest(http.MethodGet, streamableEndpointPath, nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// TestOriginRejected covers the MCP requirement to validate Origin, which
// guards against DNS rebinding.
func TestOriginRejected(t *testing.T) {
	h := newTestHTTPServer(t)

	header := http.Header{"Origin": []string{"https://evil.example.com"}}
	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, header)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	assertJSONRPCErrorIDNull(t, resp)
}

// TestNoOriginAccepted guards the other side of the Origin check: a normal MCP
// client sends no Origin, and must not be blocked by it.
func TestNoOriginAccepted(t *testing.T) {
	h := newTestHTTPServer(t)

	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestSupportedProtocolVersionAccepted checks that a version this server
// implements passes the check.
func TestSupportedProtocolVersionAccepted(t *testing.T) {
	h := newTestHTTPServer(t)

	for _, version := range mcp.ValidProtocolVersions {
		t.Run(version, func(t *testing.T) {
			header := http.Header{"Mcp-Protocol-Version": []string{version}}
			resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, header)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

// TestUnsupportedProtocolVersionRejected covers the MCP rule that a server must
// reject a version it does not implement, rather than answering as though it
// had agreed to it.
func TestUnsupportedProtocolVersionRejected(t *testing.T) {
	h := newTestHTTPServer(t)

	header := http.Header{"Mcp-Protocol-Version": []string{"1999-01-01"}}
	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, header)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	b := assertJSONRPCErrorIDNull(t, resp)
	// The body names the versions this server does support, so a client can
	// retry on one of them without guessing.
	if !strings.Contains(string(b), mcp.LATEST_PROTOCOL_VERSION) {
		t.Errorf("body %q does not name the supported versions", string(b))
	}
}

// TestMissingProtocolVersionAccepted guards the other side: the spec lets a
// server read a missing header as 2025-03-26, and mcp-go does, so the check
// must not reject a request that omits it.
func TestMissingProtocolVersionAccepted(t *testing.T) {
	h := newTestHTTPServer(t)

	resp := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestHTTPDoesNotDeclareListChanged checks that the server does not advertise a
// notification it cannot deliver. Stateless HTTP holds no client to notify.
func TestHTTPDoesNotDeclareListChanged(t *testing.T) {
	h := newTestHTTPServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	result := decodeResult(t, post(t, h, body, nil))

	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v, want an object", result["capabilities"])
	}
	tools, ok := capabilities["tools"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.tools = %v, want an object", capabilities["tools"])
	}
	if listChanged, found := tools["listChanged"]; found && listChanged == true {
		t.Error("capabilities.tools.listChanged = true, want false or absent on stateless HTTP")
	}
}
