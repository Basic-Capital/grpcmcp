package main

import (
	"net/http"
	"sync/atomic"

	"connectrpc.com/connect"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// countingHTTPClient counts the requests that pass through a connect.HTTPClient.
type countingHTTPClient struct {
	inner connect.HTTPClient
	calls atomic.Int64
}

func (c *countingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return c.inner.Do(req)
}

// TestRefreshKeepsToolsWhenReflectionMatchesNothing covers the guard against
// replacing a live tool set with an empty one. A service filter that matches
// nothing makes grpcmcp.Tools report success with no tools.
func TestRefreshKeepsToolsWhenReflectionMatchesNothing(t *testing.T) {
	backendURL := startReflectingHealthBackend(t)
	client := grpcmcp.DefaultHTTPClient(backendURL)

	descriptors, err := grpcmcp.LoadDescriptorsFromReflection(t.Context(), backendURL, nil, false, grpcmcp.WithReflectionHTTPClient(client))
	if err != nil {
		t.Fatalf("LoadDescriptorsFromReflection: %v", err)
	}
	cfg := grpcmcp.Config{
		ServerName:  "test",
		Version:     "test",
		BaseURL:     backendURL,
		HTTPClient:  client,
		Descriptors: descriptors,
	}
	srv, err := grpcmcp.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	before := len(srv.ListTools())
	if before == 0 {
		t.Fatal("expected the health tool to be registered")
	}

	// A filter that matches no method makes the rebuilt tool set empty. The
	// refresh inherits the filter from cfg, the same way main does.
	matchNothing := cfg
	matchNothing.MethodFilter = func(protoreflect.MethodDescriptor) bool { return false }

	_, err = refreshTools(t.Context(), srv, matchNothing, refreshParams{
		baseURL:         backendURL,
		useConnect:      false,
		backendClient:   client,
		methodFilter:    matchNothing.MethodFilter,
		timeout:         10 * time.Second,
		previousToolLen: before,
	})
	if err == nil {
		t.Fatal("expected an error so the caller keeps the previous tool set")
	}
	if !strings.Contains(err.Error(), "matched no tools") {
		t.Fatalf("unexpected error: %v", err)
	}
	if after := len(srv.ListTools()); after != before {
		t.Fatalf("tool set changed from %d to %d, so the guard did not hold", before, after)
	}
}

// TestRefreshAcceptsEmptyToolsWhenNoneRegistered confirms the guard only
// protects a non-empty set, so a genuinely empty backend still applies.
func TestRefreshAcceptsEmptyToolsWhenNoneRegistered(t *testing.T) {
	backendURL := startReflectingHealthBackend(t)
	client := grpcmcp.DefaultHTTPClient(backendURL)
	descriptors, err := grpcmcp.LoadDescriptorsFromReflection(t.Context(), backendURL, nil, false, grpcmcp.WithReflectionHTTPClient(client))
	if err != nil {
		t.Fatalf("LoadDescriptorsFromReflection: %v", err)
	}
	cfg := grpcmcp.Config{
		ServerName:   "test",
		Version:      "test",
		BaseURL:      backendURL,
		HTTPClient:   client,
		Descriptors:  descriptors,
		MethodFilter: func(protoreflect.MethodDescriptor) bool { return false },
	}
	srv := server.NewMCPServer("test", "test")

	count, err := refreshTools(t.Context(), srv, cfg, refreshParams{
		baseURL:         backendURL,
		backendClient:   client,
		methodFilter:    cfg.MethodFilter,
		timeout:         10 * time.Second,
		previousToolLen: 0,
	})
	if err != nil {
		t.Fatalf("refreshTools: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 tools, got %d", count)
	}
}

// TestRefreshTimesOutOnStalledBackend covers the per-attempt deadline. Without
// it, a backend that accepts the connection and then stalls parks the refresh
// loop for the life of the process.
func TestRefreshTimesOutOnStalledBackend(t *testing.T) {
	// A listener that accepts and then never speaks.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			// Hold it open without replying.
			t.Cleanup(func() { conn.Close() })
		}
	}()

	stalledURL := "http://" + l.Addr().String()
	cfg := grpcmcp.Config{
		ServerName:  "test",
		Version:     "test",
		BaseURL:     stalledURL,
		Descriptors: &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{buildEmptyFileDescriptor()}},
	}
	srv := server.NewMCPServer("test", "test")

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := refreshTools(context.Background(), srv, cfg, refreshParams{
			baseURL:       stalledURL,
			backendClient: grpcmcp.DefaultHTTPClient(stalledURL),
			timeout:       500 * time.Millisecond,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the stalled backend to produce an error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("refresh took %v, so the deadline did not apply", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refreshTools did not return, so the attempt has no deadline")
	}
}

// TestReflectionReusesSuppliedClient covers the leak fix: repeated reflection
// must reuse one client in place of building a pool per call.
func TestReflectionReusesSuppliedClient(t *testing.T) {
	backendURL := startReflectingHealthBackend(t)
	shared := &countingHTTPClient{inner: grpcmcp.DefaultHTTPClient(backendURL)}

	for range 3 {
		if _, err := grpcmcp.LoadDescriptorsFromReflection(t.Context(), backendURL, nil, false, grpcmcp.WithReflectionHTTPClient(shared)); err != nil {
			t.Fatalf("LoadDescriptorsFromReflection: %v", err)
		}
	}
	if shared.calls.Load() == 0 {
		t.Fatal("the supplied client was never used, so reflection built its own")
	}
}
