package grpcmcp

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func buildEmptyFileDescriptor() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("google/protobuf/empty.proto"),
		Package: proto.String("google.protobuf"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Empty")},
		},
	}
}

func buildTestRegistry(t *testing.T, fds *descriptorpb.FileDescriptorSet) *protoregistry.Files {
	t.Helper()
	reg := new(protoregistry.Files)
	for _, f := range fds.GetFile() {
		fd, err := protodesc.NewFile(f, reg)
		if err != nil {
			t.Fatalf("protodesc.NewFile(%q): %v", f.GetName(), err)
		}
		if _, err := reg.FindFileByPath(fd.Path()); err != nil {
			if err := reg.RegisterFile(fd); err != nil {
				t.Fatalf("RegisterFile(%q): %v", fd.Path(), err)
			}
		}
	}
	return reg
}

// methodWithOptions builds a MethodDescriptor carrying the supplied options.
func methodWithOptions(t *testing.T, opts *descriptorpb.MethodOptions) protoreflect.MethodDescriptor {
	t.Helper()
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
	reg := buildTestRegistry(t, &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{buildEmptyFileDescriptor(), fd},
	})
	fileDesc, err := reg.FindFileByPath("test_option.proto")
	if err != nil {
		t.Fatalf("failed to find file: %v", err)
	}
	return fileDesc.Services().Get(0).Methods().Get(0)
}

// methodWithVarintOption builds a MethodDescriptor that has an unknown varint
// field set to value. This exercises the filter without registering a real
// protobuf extension.
func methodWithVarintOption(t *testing.T, fieldNum uint32, value uint64) protoreflect.MethodDescriptor {
	t.Helper()
	var raw []byte
	raw = protowire.AppendTag(raw, protowire.Number(fieldNum), protowire.VarintType)
	raw = protowire.AppendVarint(raw, value)

	opts := &descriptorpb.MethodOptions{}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(raw))
	return methodWithOptions(t, opts)
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
			got := generateToolName(tt.shortNames, false, false, false, tt.pkg+"."+tt.service, tt.service, tt.method)
			if got != tt.wantName {
				t.Errorf("generateToolName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestToolNameCollisionFallback(t *testing.T) {
	// A short-name collision must fall back to the full name.
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

func TestMethodOptionFilterMatches(t *testing.T) {
	tests := []struct {
		name   string
		opts   *descriptorpb.MethodOptions
		filter *MethodOptionFilter
		want   bool
	}{
		{
			name:   "nil filter accepts every method",
			opts:   nil,
			filter: nil,
			want:   true,
		},
		{
			name:   "nil options returns false",
			opts:   nil,
			filter: &MethodOptionFilter{FieldNumber: 50003, Values: []uint64{1}},
			want:   false,
		},
		{
			name:   "empty options returns false",
			opts:   &descriptorpb.MethodOptions{},
			filter: &MethodOptionFilter{FieldNumber: 50003, Values: []uint64{1}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := methodWithOptions(t, tt.opts)
			if got := tt.filter.matches(method); got != tt.want {
				t.Errorf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMethodOptionFilterMultiValue(t *testing.T) {
	// The method sets option field 50003 to value 2.
	method := methodWithVarintOption(t, 50003, 2)

	tests := []struct {
		name   string
		values []uint64
		want   bool
	}{
		{
			name:   "value 2 matches a filter accepting 1 and 2",
			values: []uint64{1, 2},
			want:   true,
		},
		{
			name:   "value 2 does not match a filter accepting only 1",
			values: []uint64{1},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &MethodOptionFilter{FieldNumber: 50003, Values: tt.values}
			if got := filter.matches(method); got != tt.want {
				t.Errorf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseMethodOptionFilter(t *testing.T) {
	tests := []struct {
		name       string
		spec       string
		wantField  uint32
		wantValues []uint64
		wantErr    bool
	}{
		{name: "single value", spec: "50003:1", wantField: 50003, wantValues: []uint64{1}},
		{name: "multiple values", spec: "50003:1,2", wantField: 50003, wantValues: []uint64{1, 2}},
		{name: "values with spaces", spec: "50003:1, 2", wantField: 50003, wantValues: []uint64{1, 2}},
		{name: "missing separator", spec: "50003", wantErr: true},
		{name: "non numeric field", spec: "abc:1", wantErr: true},
		{name: "non numeric value", spec: "50003:abc", wantErr: true},
		{name: "zero field number", spec: "0:1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMethodOptionFilter(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMethodOptionFilter(%q) expected an error, got %+v", tt.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMethodOptionFilter(%q): %v", tt.spec, err)
			}
			if got.FieldNumber != tt.wantField {
				t.Errorf("FieldNumber = %d, want %d", got.FieldNumber, tt.wantField)
			}
			if len(got.Values) != len(tt.wantValues) {
				t.Fatalf("Values = %v, want %v", got.Values, tt.wantValues)
			}
			for i := range got.Values {
				if got.Values[i] != tt.wantValues[i] {
					t.Errorf("Values = %v, want %v", got.Values, tt.wantValues)
				}
			}
		})
	}
}
