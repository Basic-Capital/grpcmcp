package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestOperatorIdentityForwardedToBackend(t *testing.T) {
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			buildEmptyFileDescriptor(),
			buildFileDescriptor("op.test", "EchoService", []string{"Echo"}),
		},
	}
	reg := buildRegistry(fds)
	fileDesc, err := reg.FindFileByPath("op.test/EchoService.proto")
	if err != nil {
		t.Fatal(err)
	}
	method := fileDesc.Services().Get(0).Methods().Get(0)

	var gotHeader string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(operatorIdentityHeader)
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK) // empty body is a valid Empty message
	}))
	defer backend.Close()

	// Connect protocol (not gRPC) so the httptest HTTP/1.1 server works.
	c := connect.NewClient[dynamicpb.Message, dynamicpb.Message](
		backend.Client(),
		backend.URL+"/op.test.EchoService/Echo",
		connect.WithSchema(method),
		connect.WithClientOptions(responseInitializer),
	)
	handler := toolHandler(c, method.Input(), nil)

	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{}

	ctx := context.WithValue(context.Background(), operatorIdentityKey{}, "alice@basiccapital.com")
	if _, err := handler(ctx, request); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "alice@basiccapital.com" {
		t.Fatalf("expected operator identity forwarded to backend, got %q", gotHeader)
	}

	// Without the context value, no header is sent.
	gotHeader = "unset-sentinel"
	if _, err := handler(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "" {
		t.Fatalf("expected no operator identity header, got %q", gotHeader)
	}
}
