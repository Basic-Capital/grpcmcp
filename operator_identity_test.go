package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestOperatorIdentityForwardedToBackend(t *testing.T) {
	static := make(http.Header)
	static.Set("Authorization", "Bearer token")
	provider := operatorIdentityHeaders(grpcmcp.StaticHeaders(static))

	request := mcp.CallToolRequest{}
	request.Header = http.Header{}
	request.Header.Set(operatorIdentityHeader, "alice@basiccapital.com")

	h, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get(operatorIdentityHeader); got != "alice@basiccapital.com" {
		t.Fatalf("expected operator identity forwarded, got %q", got)
	}
	if got := h.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("expected static headers preserved, got %q", got)
	}

	// Without the inbound header, nothing is added.
	h, err = provider(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get(operatorIdentityHeader); got != "" {
		t.Fatalf("expected no operator identity header, got %q", got)
	}
}
