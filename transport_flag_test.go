package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestStdioIgnoresTransportFlag checks that -transport is only validated when
// -hostport is set. Before this fix, -transport was validated unconditionally,
// so a stdio invocation carrying a -transport value (even a stale or garbage
// one, which previously did nothing under stdio) would exit(-1) instead of
// serving stdio as usual.
func TestStdioIgnoresTransportFlag(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	cmd := exec.Command(bin, "--transport=not-a-real-transport", "--descriptors="+descFile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	defer cmd.Process.Kill()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("stdio process exited early (should be blocked reading stdin): %v, stderr: %s", err, stderr.String())
	case <-time.After(1 * time.Second):
		// Still running, as expected: stdio blocks on stdin regardless of
		// -transport, which only applies to the HTTP server.
	}
}

// TestStreamableHTTPAliasAccepted checks that -transport=streamable-http is
// still accepted as a synonym for -transport=http, matching this server's
// behavior before -transport was removed and then restored.
func TestStreamableHTTPAliasAccepted(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	cmd := exec.Command(bin, "--hostport=localhost:0", "--transport=streamable-http", "--descriptors="+descFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("process exited, expected -transport=streamable-http to be accepted and keep serving: %v, stderr: %s", err, stderr.String())
	case <-time.After(1 * time.Second):
		// Still running: the alias was accepted and the HTTP server started.
	}
}

// emptyDescriptorFile writes a minimal, valid FileDescriptorSet (one file, no
// services), so -descriptors parses successfully and startup proceeds far
// enough to reach the transport logic under test. A FileDescriptorSet with
// zero files trips grpcmcp's own "descriptors are required" check, a
// different failure from the one these tests care about.
func emptyDescriptorFile(t *testing.T) string {
	t.Helper()
	b, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("empty/empty.proto"),
				Package: proto.String("empty"),
				Syntax:  proto.String("proto3"),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/empty.pb"
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildGrpcmcp(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/grpcmcp"
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build grpcmcp: %v\n%s", err, out)
	}
	return bin
}
