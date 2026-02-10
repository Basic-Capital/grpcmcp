package main

import (
	"testing"

	"google.golang.org/protobuf/proto"
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
		name     string
		opts     *descriptorpb.MethodOptions
		fieldNum uint32
		expected uint64
		want     bool
	}{
		{
			name:     "nil options returns false",
			opts:     nil,
			fieldNum: 50003,
			expected: 1,
			want:     false,
		},
		{
			name:     "empty options returns false",
			opts:     &descriptorpb.MethodOptions{},
			fieldNum: 50003,
			expected: 1,
			want:     false,
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
			got := hasMethodOption(method, tt.fieldNum, tt.expected)
			if got != tt.want {
				t.Errorf("hasMethodOption() = %v, want %v", got, tt.want)
			}
		})
	}
}
