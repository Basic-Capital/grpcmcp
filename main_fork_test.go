package main

import (
	"testing"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func buildFileDescriptor(pkg string, serviceName string, methods []string) *descriptorpb.FileDescriptorProto {
	var methodDescs []*descriptorpb.MethodDescriptorProto
	for _, m := range methods {
		methodDescs = append(methodDescs, &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(m),
			InputType:  proto.String(".google.protobuf.Empty"),
			OutputType: proto.String(".google.protobuf.Empty"),
		})
	}
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String(pkg + "/" + serviceName + ".proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name:   proto.String(serviceName),
				Method: methodDescs,
			},
		},
	}
}

func buildEmptyFileDescriptor() *descriptorpb.FileDescriptorProto {
	// google/protobuf/empty.proto for dependency resolution
	fd, _ := (&emptypb.Empty{}).ProtoReflect().Descriptor().ParentFile().Options().(*descriptorpb.FileOptions)
	_ = fd
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("google/protobuf/empty.proto"),
		Package: proto.String("google.protobuf"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Empty"),
			},
		},
	}
}

// methodWithVarintOption builds a MethodDescriptor that has an unknown varint
// field (fieldNum) set to value. This lets us test hasMethodOption without
// needing a real protobuf extension registration.
func methodWithVarintOption(fieldNum uint32, value uint64) protoreflect.MethodDescriptor {
	var buf []byte
	buf = protowire.AppendTag(buf, protowire.Number(fieldNum), protowire.VarintType)
	buf = protowire.AppendVarint(buf, value)

	opts := &descriptorpb.MethodOptions{}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(buf))

	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test_option.proto"),
		Package:    proto.String("test"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("TestOptionService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("TestOptionMethod"),
						InputType:  proto.String(".google.protobuf.Empty"),
						OutputType: proto.String(".google.protobuf.Empty"),
						Options:    opts,
					},
				},
			},
		},
	}

	emptyFd := buildEmptyFileDescriptor()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{emptyFd, fd},
	}
	reg := buildRegistry(fds)
	fileDesc, err := reg.FindFileByPath("test_option.proto")
	if err != nil {
		panic("methodWithVarintOption: failed to find file: " + err.Error())
	}
	return fileDesc.Services().Get(0).Methods().Get(0)
}

func TestToolNameGeneration(t *testing.T) {
	tests := []struct {
		name       string
		shortNames bool
		pkg        string
		service    string
		method     string
		wantName   string
	}{
		{
			name:       "full name by default",
			shortNames: false,
			pkg:        "com.example.systems.wallet",
			service:    "WalletService",
			method:     "GetPlan",
			wantName:   "com_example_systems_wallet_WalletService__GetPlan",
		},
		{
			name:       "short name strips package prefix",
			shortNames: true,
			pkg:        "com.example.systems.wallet",
			service:    "WalletService",
			method:     "GetPlan",
			wantName:   "WalletService__GetPlan",
		},
		{
			name:       "full name has dots replaced with underscores",
			shortNames: false,
			pkg:        "com.example.deeply.nested.pkg",
			service:    "MyService",
			method:     "DoThing",
			wantName:   "com_example_deeply_nested_pkg_MyService__DoThing",
		},
		{
			name:       "short name has no dots to replace",
			shortNames: true,
			pkg:        "com.example.deeply.nested.pkg",
			service:    "MyService",
			method:     "DoThing",
			wantName:   "MyService__DoThing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateToolName(tt.shortNames, false, false, false, tt.pkg+"."+tt.service, tt.service, string(tt.method))
			if got != tt.wantName {
				t.Errorf("generateToolName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestToolNameCollisionFallback(t *testing.T) {
	// When short-name collision is detected, even with shortNames=true, should use full name
	got := generateToolName(true, false, true, false, "com.example.pkg1.FooService", "FooService", "GetBar")
	want := "com_example_pkg1_FooService__GetBar"
	if got != want {
		t.Errorf("generateToolName() with collision = %q, want %q", got, want)
	}
}

func TestVeryShortNames(t *testing.T) {
	tests := []struct {
		name                  string
		hasShortCollision     bool
		hasVeryShortCollision bool
		fullServiceName       string
		simpleServiceName     string
		methodName            string
		wantName              string
	}{
		{
			name:              "method-only when no collision",
			fullServiceName:   "com.example.wallet.WalletService",
			simpleServiceName: "WalletService",
			methodName:        "GetPlan",
			wantName:          "GetPlan",
		},
		{
			name:                  "falls back to service__method when method name collides",
			hasVeryShortCollision: true,
			fullServiceName:       "com.example.wallet.WalletService",
			simpleServiceName:     "WalletService",
			methodName:            "GetPlan",
			wantName:              "WalletService__GetPlan",
		},
		{
			name:                  "falls back to full name when both method and service name collide",
			hasShortCollision:     true,
			hasVeryShortCollision: true,
			fullServiceName:       "com.example.wallet.WalletService",
			simpleServiceName:     "WalletService",
			methodName:            "GetPlan",
			wantName:              "com_example_wallet_WalletService__GetPlan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateToolName(false, true, tt.hasShortCollision, tt.hasVeryShortCollision, tt.fullServiceName, tt.simpleServiceName, tt.methodName)
			if got != tt.wantName {
				t.Errorf("generateToolName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestHasMethodOption(t *testing.T) {
	tests := []struct {
		name           string
		opts           *descriptorpb.MethodOptions
		fieldNum       uint32
		expectedValues []uint64
		want           bool
	}{
		{
			name:           "nil options returns false",
			opts:           nil,
			fieldNum:       50003,
			expectedValues: []uint64{1},
			want:           false,
		},
		{
			name:           "empty options returns false",
			opts:           &descriptorpb.MethodOptions{},
			fieldNum:       50003,
			expectedValues: []uint64{1},
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &descriptorpb.FileDescriptorProto{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: proto.String("TestService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       proto.String("TestMethod"),
								InputType:  proto.String(".google.protobuf.Empty"),
								OutputType: proto.String(".google.protobuf.Empty"),
								Options:    tt.opts,
							},
						},
					},
				},
				Dependency: []string{"google/protobuf/empty.proto"},
			}

			emptyFd := buildEmptyFileDescriptor()
			fds := &descriptorpb.FileDescriptorSet{
				File: []*descriptorpb.FileDescriptorProto{emptyFd, fd},
			}

			reg := buildRegistry(fds)
			fileDesc, err := reg.FindFileByPath("test.proto")
			if err != nil {
				t.Fatalf("failed to find file: %v", err)
			}

			method := fileDesc.Services().Get(0).Methods().Get(0)
			got := hasMethodOption(method, tt.fieldNum, tt.expectedValues)
			if got != tt.want {
				t.Errorf("hasMethodOption() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasMethodOptionMultiValue(t *testing.T) {
	// Method has option field 50003 set to value 2.
	method := methodWithVarintOption(50003, 2)

	tests := []struct {
		name           string
		expectedValues []uint64
		want           bool
	}{
		{
			name:           "value 2 matches slice containing 1 and 2",
			expectedValues: []uint64{1, 2},
			want:           true,
		},
		{
			name:           "value 2 does not match slice containing only 1",
			expectedValues: []uint64{1},
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasMethodOption(method, 50003, tt.expectedValues)
			if got != tt.want {
				t.Errorf("hasMethodOption() = %v, want %v", got, tt.want)
			}
		})
	}
}

func methodWithStringOption(fieldNum uint32, value string) protoreflect.MethodDescriptor {
	var buf []byte
	buf = protowire.AppendTag(buf, protowire.Number(fieldNum), protowire.BytesType)
	buf = protowire.AppendString(buf, value)

	opts := &descriptorpb.MethodOptions{}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(buf))

	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test_string_option.proto"),
		Package:    proto.String("test"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("TestStringOptionService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("TestStringOptionMethod"),
						InputType:  proto.String(".google.protobuf.Empty"),
						OutputType: proto.String(".google.protobuf.Empty"),
						Options:    opts,
					},
				},
			},
		},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(emptypb.File_google_protobuf_empty_proto),
			fd,
		},
	})
	if err != nil {
		panic(err)
	}
	d, err := files.FindDescriptorByName("test.TestStringOptionService.TestStringOptionMethod")
	if err != nil {
		panic(err)
	}
	return d.(protoreflect.MethodDescriptor)
}

func TestGetMethodOptionString(t *testing.T) {
	t.Run("returns the string option value", func(t *testing.T) {
		m := methodWithStringOption(50005, "Returns the plan for a UUID.")
		if got := getMethodOptionString(m, 50005); got != "Returns the plan for a UUID." {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("returns empty for a different field number", func(t *testing.T) {
		m := methodWithStringOption(50005, "x")
		if got := getMethodOptionString(m, 50006); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("returns empty when the field is a varint, not a string", func(t *testing.T) {
		m := methodWithVarintOption(50003, 1)
		if got := getMethodOptionString(m, 50003); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("returns empty when the option is absent", func(t *testing.T) {
		m := methodWithVarintOption(50003, 1)
		if got := getMethodOptionString(m, 50005); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestToolsUseMethodDescriptionOption(t *testing.T) {
	m := methodWithStringOption(50005, "Returns the plan for a UUID.")
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(emptypb.File_google_protobuf_empty_proto),
			protodesc.ToFileDescriptorProto(m.ParentFile()),
		},
	}
	tools, err := grpcmcp.Tools(grpcmcp.Config{
		Descriptors: fds,
		BaseURL:     "http://127.0.0.1:1",
		MethodDescription: func(md protoreflect.MethodDescriptor) string {
			return getMethodOptionString(md, 50005)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if got := tools[0].Tool.Description; got != "Returns the plan for a UUID." {
		t.Fatalf("description = %q", got)
	}
}
