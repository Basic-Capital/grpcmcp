package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// refreshParams carries the settings that one refresh attempt needs.
type refreshParams struct {
	baseURL        string
	headers        http.Header
	useConnect     bool
	backendClient  connect.HTTPClient
	servicesMap    map[string]struct{}
	methodFilter   func(protoreflect.MethodDescriptor) bool
	shortNames     bool
	veryShortNames bool
	timeout        time.Duration
	// previousToolLen is how many tools the server holds now. A refresh that
	// would drop a non-empty set to zero is skipped.
	previousToolLen int
}

// refreshTools reflects the backend once and replaces the server's tool set. It
// returns how many tools it registered.
//
// It reports an error and leaves the tool set alone when reflection fails, when
// the tool build fails, or when the result would empty a non-empty tool set.
func refreshTools(ctx context.Context, srv *server.MCPServer, cfg grpcmcp.Config, p refreshParams) (int, error) {
	// Bound the attempt. Neither the shared HTTP client nor the process context
	// sets a deadline, so a backend that accepts the connection and then stalls
	// would park this call, and the loop that calls it, for good.
	attemptCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	newDescriptors, err := grpcmcp.LoadDescriptorsFromReflection(
		attemptCtx, p.baseURL, p.headers, p.useConnect,
		grpcmcp.WithReflectionHTTPClient(p.backendClient),
	)
	if err != nil {
		return 0, err
	}

	refreshCfg := cfg
	refreshCfg.Descriptors = newDescriptors
	refreshCfg.ToolName = buildToolNamer(newDescriptors, p.servicesMap, p.methodFilter, p.shortNames, p.veryShortNames)
	tools, err := grpcmcp.Tools(refreshCfg)
	if err != nil {
		return 0, err
	}

	// grpcmcp.Tools reports success with no tools when the descriptors parse but
	// the service filter matches nothing, which happens while a backend rolls.
	// Replacing a live tool set with an empty one would take every tool away
	// from each connected client, so keep what we have.
	if len(tools) == 0 && p.previousToolLen > 0 {
		return 0, fmt.Errorf("reflection matched no tools, keeping the %d in place", p.previousToolLen)
	}

	srv.SetTools(tools...)
	return len(tools), nil
}
