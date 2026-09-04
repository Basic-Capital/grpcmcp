package grpcmcp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/Basic-Capital/grpcmcp/buf"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var protojsonMarshaller = protojson.MarshalOptions{UseProtoNames: true}
var protojsonUnmarshaller = protojson.UnmarshalOptions{DiscardUnknown: true}

// ToolHeaderProvider provides headers for outbound backend gRPC requests.
type ToolHeaderProvider func(context.Context, mcp.CallToolRequest) (http.Header, error)

// Config configures an MCP server backed by a gRPC descriptor set.
type Config struct {
	ServerName  string
	Version     string
	BaseURL     string
	HTTPClient  connect.HTTPClient
	UseConnect  bool
	Descriptors *descriptorpb.FileDescriptorSet
	Services    []protoreflect.FullName
	Headers     ToolHeaderProvider
	String64    bool
	// MethodFilter, when set, limits which unary methods are exposed as
	// tools; methods for which it returns false are skipped.
	MethodFilter func(protoreflect.MethodDescriptor) bool
	// ToolName, when set, overrides the default tool naming
	// (full service name + "__" + method name, dots replaced).
	ToolName func(protoreflect.ServiceDescriptor, protoreflect.MethodDescriptor) string
	// MethodDescription, when set, supplies an extra tool description for a
	// method (for example from a custom method option). A non-empty result is
	// placed before any descriptor source comments, which are usually absent
	// from descriptors served over reflection by Java backends.
	MethodDescription func(protoreflect.MethodDescriptor) string
	// ServerOptions are passed through to server.NewMCPServer.
	ServerOptions []server.ServerOption
}

// StaticHeaders returns a ToolHeaderProvider that always returns the supplied
// headers. The returned headers are cloned for each call.
func StaticHeaders(headers http.Header) ToolHeaderProvider {
	return func(context.Context, mcp.CallToolRequest) (http.Header, error) {
		return headers.Clone(), nil
	}
}

// LoadDescriptorsFromFile loads a protobuf FileDescriptorSet from disk.
func LoadDescriptorsFromFile(path string) (*descriptorpb.FileDescriptorSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(b, &fds); err != nil {
		return nil, err
	}
	return &fds, nil
}

// ReflectionOption configures LoadDescriptorsFromReflection.
type ReflectionOption func(*reflectionConfig)

type reflectionConfig struct {
	httpClient connect.HTTPClient
}

// WithReflectionHTTPClient reuses client for the reflection calls in place of
// building a new one. A caller that reflects repeatedly should pass the same
// client every time: each new client brings its own connection pool, and an
// HTTP/2 connection that no longer has an owner still holds a goroutine and a
// file descriptor until the peer closes it.
func WithReflectionHTTPClient(client connect.HTTPClient) ReflectionOption {
	return func(cfg *reflectionConfig) {
		cfg.httpClient = client
	}
}

// DefaultHTTPClient returns the HTTP client that backend calls to baseURL use
// when the caller supplies none. Build it once and share it to keep one
// connection pool for the process.
func DefaultHTTPClient(baseURL string) connect.HTTPClient {
	return backendHTTPClient(baseURL, nil)
}

// LoadDescriptorsFromReflection loads a protobuf FileDescriptorSet from a gRPC
// server that has reflection enabled.
func LoadDescriptorsFromReflection(ctx context.Context, baseURL string, headers http.Header, useConnect bool, opts ...ReflectionOption) (*descriptorpb.FileDescriptorSet, error) {
	var rcfg reflectionConfig
	for _, opt := range opts {
		opt(&rcfg)
	}
	httpClient := backendHTTPClient(baseURL, rcfg.httpClient)
	connectOpts := connectOptions(useConnect)

	all := map[string]*descriptorpb.FileDescriptorProto{}
	client := grpcreflect.NewClient(httpClient, baseURL, connectOpts...)
	stream := client.NewStream(ctx, grpcreflect.WithRequestHeaders(headers.Clone()))
	closed := false
	defer func() {
		if !closed {
			stream.Close()
		}
	}()
	names, err := stream.ListServices()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		fileDescriptors, err := stream.FileContainingSymbol(name)
		if err != nil {
			return nil, err
		}
		for _, d := range fileDescriptors {
			all[d.GetName()] = d
		}
	}
	_, err = stream.Close()
	closed = true
	if err != nil {
		return nil, err
	}

	var ds []*descriptorpb.FileDescriptorProto
	seen := map[string]struct{}{}
	for _, d := range all {
		if err := topSort(d, all, seen, &ds); err != nil {
			return nil, err
		}
	}
	return &descriptorpb.FileDescriptorSet{File: ds}, nil
}

// NewServer builds an MCP server that exposes unary gRPC methods as tools.
func NewServer(cfg Config) (*server.MCPServer, error) {
	tools, err := Tools(cfg)
	if err != nil {
		return nil, err
	}
	srv := server.NewMCPServer(cfg.ServerName, cfg.Version, cfg.ServerOptions...)
	srv.AddTools(tools...)
	return srv, nil
}

// Tools builds the MCP tools for cfg without constructing a server. It can be
// used with server.MCPServer.SetTools to replace the tool set when the
// backend's schema changes.
func Tools(cfg Config) ([]server.ServerTool, error) {
	if cfg.Descriptors == nil || len(cfg.Descriptors.GetFile()) == 0 {
		return nil, fmt.Errorf("descriptors are required")
	}
	if cfg.Headers == nil {
		cfg.Headers = StaticHeaders(nil)
	}

	servicesMap := map[protoreflect.FullName]struct{}{}
	for _, service := range cfg.Services {
		servicesMap[service] = struct{}{}
	}

	httpClient := backendHTTPClient(cfg.BaseURL, cfg.HTTPClient)
	connectOpts := connectOptions(cfg.UseConnect)

	var tools []server.ServerTool
	reg := new(protoregistry.Files)
	for _, f := range cfg.Descriptors.GetFile() {
		desc, err := protodesc.NewFile(f, reg)
		if err != nil {
			return nil, err
		}
		if _, err := reg.FindFileByPath(desc.Path()); err != nil {
			if err := reg.RegisterFile(desc); err != nil {
				return nil, err
			}
		}
		services := desc.Services()
		for i := range services.Len() {
			s := services.Get(i)
			if len(servicesMap) > 0 {
				if _, found := servicesMap[s.FullName()]; !found {
					continue
				}
			}
			methods := s.Methods()
			for j := range methods.Len() {
				m := methods.Get(j)
				if m.IsStreamingClient() || m.IsStreamingServer() {
					// Backend gRPC streaming methods are not exposed as MCP tools.
					continue
				}
				if cfg.MethodFilter != nil && !cfg.MethodFilter(m) {
					continue
				}
				var schemaOpts []buf.GeneratorOption
				if cfg.String64 {
					schemaOpts = append(schemaOpts, buf.WithStringOnly64BitIntegers())
				}
				input := buf.Generate(m.Input(), schemaOpts...)
				j, err := json.Marshal(input)
				if err != nil {
					return nil, err
				}
				var rawJSON json.RawMessage
				if err := rawJSON.UnmarshalJSON(j); err != nil {
					return nil, err
				}
				src := desc.SourceLocations().ByDescriptor(m)
				var descriptions []string
				if cfg.MethodDescription != nil {
					if d := strings.TrimSpace(cfg.MethodDescription(m)); d != "" {
						descriptions = append(descriptions, d)
					}
				}
				if src.LeadingComments != "" {
					descriptions = append(descriptions, strings.TrimSpace(src.LeadingComments))
				}
				if src.TrailingComments != "" {
					descriptions = append(descriptions, strings.TrimSpace(src.TrailingComments))
				}
				procedure := fmt.Sprintf("/%v/%v", s.FullName(), m.Name())
				description := strings.Join(descriptions, " | ")
				c := connect.NewClient[dynamicpb.Message, dynamicpb.Message](httpClient, cfg.BaseURL+procedure, connect.WithSchema(m), connect.WithClientOptions(connectOpts...))

				name := strings.ReplaceAll(fmt.Sprintf("%v__%v", s.FullName(), m.Name()), ".", "_")
				if cfg.ToolName != nil {
					name = cfg.ToolName(s, m)
				}
				tools = append(tools, server.ServerTool{
					Tool:    mcp.NewToolWithRawSchema(name, description, rawJSON),
					Handler: toolHandler(c, m.Input(), cfg.Headers),
				})
			}
		}
	}

	return tools, nil
}

func toolHandler(c *connect.Client[dynamicpb.Message, dynamicpb.Message], desc protoreflect.MessageDescriptor, headers ToolHeaderProvider) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		msg := dynamicpb.NewMessage(desc)
		b, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if err := protojsonUnmarshaller.Unmarshal(b, msg); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		req := connect.NewRequest(msg)
		outboundHeaders, err := headers(ctx, request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		addHeaders(req.Header(), outboundHeaders)
		resp, err := c.CallUnary(ctx, req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := protojsonMarshaller.Marshal(resp.Msg)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(res)), nil
	}
}

func addHeaders(dst http.Header, src http.Header) {
	for k, v := range src {
		if len(v) == 1 {
			dst.Set(k, v[0])
		} else {
			dst.Del(k)
			for _, v2 := range v {
				dst.Add(k, v2)
			}
		}
	}
}

func insecureClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
	}
}

func backendHTTPClient(baseURL string, configured connect.HTTPClient) connect.HTTPClient {
	if configured != nil {
		return configured
	}
	if strings.HasPrefix(baseURL, "http://") {
		return insecureClient()
	}
	return http.DefaultClient
}

var responseInitializer = connect.WithResponseInitializer(func(spec connect.Spec, message any) error {
	if m, ok := message.(*dynamicpb.Message); ok {
		desc := spec.Schema.(protoreflect.MethodDescriptor)
		*m = *dynamicpb.NewMessage(desc.Output())
	}
	return nil
})

func connectOptions(useConnect bool) []connect.ClientOption {
	connectOpts := []connect.ClientOption{responseInitializer}
	if !useConnect {
		connectOpts = append(connectOpts, connect.WithGRPC())
	}
	return connectOpts
}

func topSort(d *descriptorpb.FileDescriptorProto, all map[string]*descriptorpb.FileDescriptorProto, seen map[string]struct{}, ds *[]*descriptorpb.FileDescriptorProto) error {
	if _, found := seen[d.GetName()]; found {
		return nil
	}
	seen[d.GetName()] = struct{}{}
	for _, dep := range d.GetDependency() {
		v, found := all[dep]
		if !found {
			return fmt.Errorf("descriptor %q depends on missing descriptor %q", d.GetName(), dep)
		}
		if err := topSort(v, all, seen, ds); err != nil {
			return err
		}
	}
	*ds = append(*ds, d)
	return nil
}
