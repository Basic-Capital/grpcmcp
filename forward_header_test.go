package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestForwardHeadersCopiesNamedHeaders(t *testing.T) {
	static := make(http.Header)
	static.Set("Authorization", "Bearer token")
	provider := forwardHeaders(grpcmcp.StaticHeaders(static), []string{"X-Forwarded-User", "X-Forwarded-Access-Token"})

	request := mcp.CallToolRequest{}
	request.Header = http.Header{}
	request.Header.Set("X-Forwarded-User", "alice@basiccapital.com")
	request.Header.Set("X-Forwarded-Access-Token", "opaque-token")
	request.Header.Set("X-Forwarded-Email", "alice@basiccapital.com") // not in the forward list

	h, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("X-Forwarded-User"); got != "alice@basiccapital.com" {
		t.Errorf("X-Forwarded-User = %q, want alice@basiccapital.com", got)
	}
	if got := h.Get("X-Forwarded-Access-Token"); got != "opaque-token" {
		t.Errorf("X-Forwarded-Access-Token = %q, want opaque-token", got)
	}
	if got := h.Get("X-Forwarded-Email"); got != "" {
		t.Errorf("X-Forwarded-Email = %q, want empty: not in the configured forward list", got)
	}
	if got := h.Get("Authorization"); got != "Bearer token" {
		t.Errorf("Authorization = %q, want static headers preserved", got)
	}

	// Without any inbound headers, nothing is added.
	h, err = provider(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("X-Forwarded-User"); got != "" {
		t.Errorf("X-Forwarded-User = %q, want empty", got)
	}
}
