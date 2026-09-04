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

// buildServerStreamingFileDescriptor is like buildFileDescriptor but marks the
// single method as server-streaming, so buildToolNamer must skip it.
func buildServerStreamingFileDescriptor(pkg, serviceName, methodName string) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String(pkg + "/" + serviceName + ".proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String(serviceName),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:            proto.String(methodName),
						InputType:       proto.String(".google.protobuf.Empty"),
						OutputType:      proto.String(".google.protobuf.Empty"),
						ServerStreaming: proto.Bool(true),
					},
				},
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

// TestBuildToolNamerSkipsServicesWithNoExposedMethods guards the short-name
// collision scan: a same-named sibling that only has streaming (or filtered-out)
// methods must not force the full-path fallback.
func TestBuildToolNamerSkipsServicesWithNoExposedMethods(t *testing.T) {
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			buildEmptyFileDescriptor(),
			buildFileDescriptor("pkg1", "WalletService", []string{"GetPlan"}),
			buildFileDescriptor("pkg3", "OtherService", []string{"GetPlan"}),
			// Same simple name as pkg1, but nothing exposed — must not count.
			buildServerStreamingFileDescriptor("pkg2", "WalletService", "WatchPlan"),
		},
	}
	namer := buildToolNamer(fds, nil, nil)
	reg := buildRegistry(fds)
	fd, err := reg.FindFileByPath("pkg1/WalletService.proto")
	if err != nil {
		t.Fatalf("FindFileByPath: %v", err)
	}
	s := fd.Services().Get(0)
	m := s.Methods().Get(0)
	got := namer(s, m)
	if got != "WalletService__GetPlan" {
		t.Errorf("namer() = %q, want %q (streaming sibling must not force full path)", got, "WalletService__GetPlan")
	}
}

func TestToolNameGeneration(t *testing.T) {
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
		{
			name:                  "full name replaces every dot with an underscore",
			hasShortCollision:     true,
			hasVeryShortCollision: true,
			fullServiceName:       "com.example.deeply.nested.pkg.MyService",
			simpleServiceName:     "MyService",
			methodName:            "DoThing",
			wantName:              "com_example_deeply_nested_pkg_MyService__DoThing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateToolName(tt.hasShortCollision, tt.hasVeryShortCollision, tt.fullServiceName, tt.simpleServiceName, tt.methodName)
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

func TestGetMethodOptionStringUTF8(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty"},
		{name: "multibyte text", value: "Café 世界 🌍", want: "Café 世界 🌍"},
		{name: "invalid byte", value: "description\xff"},
		{name: "truncated sequence", value: "description\xe2\x82"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := methodWithStringOption(50005, tc.value)
			if got := getMethodOptionString(m, 50005); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
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

func TestMethodOptionLookupSkipsFields(t *testing.T) {
	var unrelated []byte
	unrelated = protowire.AppendTag(unrelated, 50001, protowire.VarintType)
	unrelated = protowire.AppendVarint(unrelated, 42)
	unrelated = protowire.AppendTag(unrelated, 50002, protowire.Fixed32Type)
	unrelated = protowire.AppendFixed32(unrelated, 42)
	unrelated = protowire.AppendTag(unrelated, 50003, protowire.Fixed64Type)
	unrelated = protowire.AppendFixed64(unrelated, 42)
	unrelated = protowire.AppendTag(unrelated, 50004, protowire.BytesType)
	unrelated = protowire.AppendString(unrelated, "unrelated")
	unrelated = protowire.AppendTag(unrelated, 50006, protowire.StartGroupType)
	group := protowire.AppendTag(nil, 1, protowire.VarintType)
	group = protowire.AppendVarint(group, 42)
	unrelated = protowire.AppendGroup(unrelated, 50006, group)

	malformed := protowire.AppendTag(nil, 50004, protowire.BytesType)
	malformed = protowire.AppendVarint(malformed, 1000) // Length exceeds available bytes.

	for _, tc := range []struct {
		name   string
		prefix []byte
		suffix []byte
		found  bool
	}{
		{name: "all wire types before match", prefix: unrelated, found: true},
		{name: "malformed field before match", prefix: malformed},
		{name: "malformed tag before match", prefix: []byte{0}},
		{name: "stop before malformed suffix", prefix: unrelated, suffix: malformed, found: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stringOption := range []bool{false, true} {
				m := methodWithVarintOption(50005, 7)
				if stringOption {
					m = methodWithStringOption(50005, "description")
				}
				opts := m.Options().ProtoReflect()
				wire := append([]byte(nil), tc.prefix...)
				wire = append(wire, opts.GetUnknown()...)
				wire = append(wire, tc.suffix...)
				opts.SetUnknown(wire)
				if stringOption {
					want := ""
					if tc.found {
						want = "description"
					}
					if got := getMethodOptionString(m, 50005); got != want {
						t.Fatalf("string option = %q, want %q", got, want)
					}
				} else if got := hasMethodOption(m, 50005, []uint64{7}); got != tc.found {
					t.Fatalf("varint option found = %v, want %v", got, tc.found)
				}
			}
		})
	}
}
