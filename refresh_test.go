package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/protobuf/types/descriptorpb"
)

// listToolNames sends a tools/list JSON-RPC request to the server and returns the tool names.
func listToolNames(t *testing.T, srv *server.MCPServer) []string {
	t.Helper()
	msg, err := json.Marshal(mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcp.NewRequestId(1),
		Request: mcp.Request{Method: "tools/list"},
	})
	if err != nil {
		t.Fatalf("marshal tools/list request: %v", err)
	}
	resp := srv.HandleMessage(context.Background(), msg)
	jsonResp, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T", resp)
	}
	listResult, ok := jsonResp.Result.(mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", jsonResp.Result)
	}
	var names []string
	for _, tool := range listResult.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// buildToolsFromFds builds MCP ServerTools from a FileDescriptorSet the same
// way the refresh goroutine does.
func buildToolsFromFds(t *testing.T, fds *descriptorpb.FileDescriptorSet) []server.ServerTool {
	t.Helper()
	tools, err := grpcmcp.Tools(grpcmcp.Config{
		Descriptors: fds,
		BaseURL:     "http://localhost:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestRefreshReplacesTools(t *testing.T) {
	emptyFd := buildEmptyFileDescriptor()

	// Initial: WalletService with one method.
	initialFds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			emptyFd,
			buildFileDescriptor("com.example.wallet", "WalletService", []string{"GetPlan"}),
		},
	}

	srv := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(true))
	srv.AddTools(buildToolsFromFds(t, initialFds)...)

	names := listToolNames(t, srv)
	if len(names) != 1 {
		t.Fatalf("expected 1 tool after initial registration, got %d: %v", len(names), names)
	}
	if names[0] != "com_example_wallet_WalletService__GetPlan" {
		t.Errorf("unexpected tool name: %s", names[0])
	}

	// Simulate refresh: WalletService now has two methods (a new RPC was deployed).
	refreshedFds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			emptyFd,
			buildFileDescriptor("com.example.wallet", "WalletService", []string{"GetPlan", "CreatePlan"}),
		},
	}

	// Atomically replace tools, same as the refresh goroutine does.
	srv.SetTools(buildToolsFromFds(t, refreshedFds)...)

	names = listToolNames(t, srv)
	if len(names) != 2 {
		t.Fatalf("expected 2 tools after refresh, got %d: %v", len(names), names)
	}
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["com_example_wallet_WalletService__GetPlan"] {
		t.Error("missing GetPlan after refresh")
	}
	if !nameSet["com_example_wallet_WalletService__CreatePlan"] {
		t.Error("missing CreatePlan after refresh")
	}
}

func TestRefreshRemovesTools(t *testing.T) {
	emptyFd := buildEmptyFileDescriptor()

	// Initial: two services.
	initialFds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			emptyFd,
			buildFileDescriptor("com.example.wallet", "WalletService", []string{"GetPlan"}),
			buildFileDescriptor("com.example.billing", "BillingService", []string{"GetInvoice"}),
		},
	}

	srv := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(true))
	srv.AddTools(buildToolsFromFds(t, initialFds)...)

	names := listToolNames(t, srv)
	if len(names) != 2 {
		t.Fatalf("expected 2 tools initially, got %d: %v", len(names), names)
	}

	// Simulate refresh: BillingService was removed.
	refreshedFds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			emptyFd,
			buildFileDescriptor("com.example.wallet", "WalletService", []string{"GetPlan"}),
		},
	}

	srv.SetTools(buildToolsFromFds(t, refreshedFds)...)

	names = listToolNames(t, srv)
	if len(names) != 1 {
		t.Fatalf("expected 1 tool after refresh, got %d: %v", len(names), names)
	}
	if names[0] != "com_example_wallet_WalletService__GetPlan" {
		t.Errorf("unexpected tool name: %s", names[0])
	}
}
