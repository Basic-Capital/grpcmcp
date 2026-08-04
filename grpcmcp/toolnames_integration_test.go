package grpcmcp

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// serviceFile builds a descriptor for one service with the named unary methods.
// A method name may carry an option value, supplied through optionValues.
func serviceFile(pkg string, serviceName string, methods []string, optionField uint32, optionValues map[string]uint64) *descriptorpb.FileDescriptorProto {
	methodDescs := make([]*descriptorpb.MethodDescriptorProto, 0, len(methods))
	for _, m := range methods {
		md := &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(m),
			InputType:  proto.String(".google.protobuf.Empty"),
			OutputType: proto.String(".google.protobuf.Empty"),
		}
		if value, ok := optionValues[m]; ok {
			var raw []byte
			raw = protowire.AppendTag(raw, protowire.Number(optionField), protowire.VarintType)
			raw = protowire.AppendVarint(raw, value)
			opts := &descriptorpb.MethodOptions{}
			opts.ProtoReflect().SetUnknown(protoreflect.RawFields(raw))
			md.Options = opts
		}
		methodDescs = append(methodDescs, md)
	}
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String(pkg + "/" + serviceName + ".proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: proto.String(serviceName), Method: methodDescs},
		},
	}
}

// registeredToolNames builds a server from cfg and returns its tool names.
func registeredToolNames(t *testing.T, cfg Config) []string {
	t.Helper()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tools := srv.ListTools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Tool.Name)
	}
	slices.Sort(names)
	return names
}

func descriptorSet(files ...*descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorSet {
	all := []*descriptorpb.FileDescriptorProto{buildEmptyFileDescriptor()}
	all = append(all, files...)
	return &descriptorpb.FileDescriptorSet{File: all}
}

func TestNewServerAppliesShortNames(t *testing.T) {
	fds := descriptorSet(
		serviceFile("com.example.wallet", "WalletService", []string{"GetPlan"}, 0, nil),
	)

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "full names by default",
			cfg:  Config{},
			want: []string{"com_example_wallet_WalletService__GetPlan"},
		},
		{
			name: "short names drop the package",
			cfg:  Config{ShortNames: true},
			want: []string{"WalletService__GetPlan"},
		},
		{
			name: "very short names drop the service",
			cfg:  Config{VeryShortNames: true},
			want: []string{"GetPlan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.Descriptors = fds
			got := registeredToolNames(t, cfg)
			if !slices.Equal(got, tt.want) {
				t.Errorf("tool names = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewServerShortNamesFallBackOnCollision(t *testing.T) {
	// Two services share the simple name WalletService, so short names must fall
	// back to the full package path.
	fds := descriptorSet(
		serviceFile("com.example.a", "WalletService", []string{"GetPlan"}, 0, nil),
		serviceFile("com.example.b", "WalletService", []string{"GetQuote"}, 0, nil),
	)
	got := registeredToolNames(t, Config{Descriptors: fds, ShortNames: true})
	want := []string{
		"com_example_a_WalletService__GetPlan",
		"com_example_b_WalletService__GetQuote",
	}
	if !slices.Equal(got, want) {
		t.Errorf("tool names = %v, want %v", got, want)
	}
}

func TestNewServerVeryShortNamesFallBackOnMethodCollision(t *testing.T) {
	// Both services define GetPlan, so that method falls back to the service
	// prefix while the unique method keeps the very short form.
	fds := descriptorSet(
		serviceFile("com.example.a", "WalletService", []string{"GetPlan"}, 0, nil),
		serviceFile("com.example.b", "QuoteService", []string{"GetPlan", "GetQuote"}, 0, nil),
	)
	got := registeredToolNames(t, Config{Descriptors: fds, VeryShortNames: true})
	want := []string{
		"QuoteService__GetPlan",
		"GetQuote",
		"WalletService__GetPlan",
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("tool names = %v, want %v", got, want)
	}
}

func TestNewServerFiltersByMethodOption(t *testing.T) {
	fds := descriptorSet(
		serviceFile("com.example.wallet", "WalletService",
			[]string{"Exposed", "AlsoExposed", "Hidden", "NoOption"},
			50003,
			map[string]uint64{"Exposed": 1, "AlsoExposed": 2, "Hidden": 9},
		),
	)

	tests := []struct {
		name   string
		values []uint64
		want   []string
	}{
		{
			name:   "single accepted value",
			values: []uint64{1},
			want:   []string{"com_example_wallet_WalletService__Exposed"},
		},
		{
			name:   "multiple accepted values",
			values: []uint64{1, 2},
			want: []string{
				"com_example_wallet_WalletService__AlsoExposed",
				"com_example_wallet_WalletService__Exposed",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registeredToolNames(t, Config{
				Descriptors:  fds,
				MethodOption: &MethodOptionFilter{FieldNumber: 50003, Values: tt.values},
			})
			if !slices.Equal(got, tt.want) {
				t.Errorf("tool names = %v, want %v", got, tt.want)
			}
		})
	}
}

// A filtered-out method must not influence very-short-name collision counting.
func TestNewServerMethodOptionFilterAffectsCollisionCounting(t *testing.T) {
	fds := descriptorSet(
		serviceFile("com.example.a", "WalletService", []string{"GetPlan"}, 50003, map[string]uint64{"GetPlan": 1}),
		serviceFile("com.example.b", "QuoteService", []string{"GetPlan"}, 50003, map[string]uint64{"GetPlan": 9}),
	)
	// Only service a's GetPlan passes the filter, so no collision remains and the
	// very short name applies.
	got := registeredToolNames(t, Config{
		Descriptors:    fds,
		VeryShortNames: true,
		MethodOption:   &MethodOptionFilter{FieldNumber: 50003, Values: []uint64{1}},
	})
	want := []string{"GetPlan"}
	if !slices.Equal(got, want) {
		t.Errorf("tool names = %v, want %v", got, want)
	}
}
