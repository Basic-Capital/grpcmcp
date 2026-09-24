package grpcmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTopSortReturnsErrorForMissingDependency(t *testing.T) {
	desc := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test.proto"),
		Dependency: []string{"missing.proto"},
	}

	var sorted []*descriptorpb.FileDescriptorProto
	err := topSort(desc, map[string]*descriptorpb.FileDescriptorProto{
		desc.GetName(): desc,
	}, map[string]struct{}{}, &sorted)
	if err == nil {
		t.Fatal("expected missing dependency error")
	}
	if !strings.Contains(err.Error(), `depends on missing descriptor "missing.proto"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestToolsAnnotateFromIdempotencyLevel(t *testing.T) {
	method := func(name string, level *descriptorpb.MethodOptions_IdempotencyLevel) *descriptorpb.MethodDescriptorProto {
		m := &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String(".google.protobuf.Empty"),
			OutputType: proto.String(".google.protobuf.Empty"),
		}
		if level != nil {
			m.Options = &descriptorpb.MethodOptions{IdempotencyLevel: level}
		}
		return m
	}
	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test_idempotency.proto"),
		Package:    proto.String("test"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("TestService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Read", descriptorpb.MethodOptions_NO_SIDE_EFFECTS.Enum()),
				method("Put", descriptorpb.MethodOptions_IDEMPOTENT.Enum()),
				method("Unknown", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN.Enum()),
				method("Write", nil),
			},
		}},
	}
	tools, err := Tools(Config{
		Descriptors: &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(emptypb.File_google_protobuf_empty_proto),
			fd,
		}},
		BaseURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, tool := range tools {
		b, err := json.Marshal(tool.Tool.Annotations)
		if err != nil {
			t.Fatal(err)
		}
		got[tool.Tool.Name] = string(b)
	}
	want := map[string]string{
		"test_TestService__Read":    `{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true}`,
		"test_TestService__Put":     `{"readOnlyHint":false,"idempotentHint":true}`,
		"test_TestService__Unknown": `{}`,
		"test_TestService__Write":   `{}`,
	}
	if len(got) != len(want) {
		t.Fatalf("got tools %v, want %v", got, want)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s annotations = %s, want %s", name, got[name], w)
		}
	}
}
