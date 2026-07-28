package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Basic-Capital/grpcmcp/grpcmcp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// operatorIdentityHeader carries the operator's identity, minted by a trusted
// edge proxy that verified the operator's client cert. It is copied from each
// inbound MCP request onto the outbound gRPC call so the backend can
// attribute the call to the human operator behind the agent.
const operatorIdentityHeader = "X-Operator-Identity"

// operatorIdentityHeaders wraps base to copy the X-Operator-Identity header
// from the inbound MCP request onto the outbound gRPC call.
func operatorIdentityHeaders(base grpcmcp.ToolHeaderProvider) grpcmcp.ToolHeaderProvider {
	return func(ctx context.Context, req mcp.CallToolRequest) (http.Header, error) {
		h, err := base(ctx, req)
		if err != nil {
			return nil, err
		}
		if v := req.Header.Get(operatorIdentityHeader); v != "" {
			h.Set(operatorIdentityHeader, v)
		}
		return h, nil
	}
}

type headerFlags http.Header

func (s *headerFlags) String() string {
	return fmt.Sprintf("%v", *s)
}

func (s *headerFlags) Set(value string) error {
	k, v, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("err invalid header format: expecting `key: value`")
	}
	h := http.Header(*s)
	h.Add(k, strings.TrimLeft(v, " "))
	return nil
}

func generateToolName(shortNames bool, veryShortNames bool, hasShortCollision bool, hasVeryShortCollision bool, fullServiceName string, simpleServiceName string, methodName string) string {
	if veryShortNames && !hasVeryShortCollision {
		return methodName
	}
	if (shortNames || veryShortNames) && !hasShortCollision {
		return fmt.Sprintf("%v__%v", simpleServiceName, methodName)
	}
	return strings.ReplaceAll(fmt.Sprintf("%v__%v", fullServiceName, methodName), ".", "_")
}

// buildToolNamer pre-scans for collisions so --short-names / --very-short-names
// can fall back: services sharing a simple name keep the full path, and method
// names defined by multiple services keep the service prefix. Streaming and
// filtered-out methods are skipped to match the actual set of registered tools.
// Returns nil (default naming) when neither flag is set.
func buildToolNamer(fds *descriptorpb.FileDescriptorSet, servicesMap map[string]struct{}, methodFilter func(protoreflect.MethodDescriptor) bool, shortNames bool, veryShortNames bool) func(protoreflect.ServiceDescriptor, protoreflect.MethodDescriptor) string {
	if !shortNames && !veryShortNames {
		return nil
	}
	simpleNameCount := map[string]int{}
	methodNameCount := map[string]int{}
	scanReg := buildRegistry(fds)
	scanReg.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			s := fd.Services().Get(i)
			if len(servicesMap) > 0 {
				if _, found := servicesMap[string(s.FullName())]; !found {
					continue
				}
			}
			simpleNameCount[string(s.Name())]++
			if veryShortNames {
				for j := range s.Methods().Len() {
					m := s.Methods().Get(j)
					if m.IsStreamingClient() || m.IsStreamingServer() {
						continue
					}
					if methodFilter != nil && !methodFilter(m) {
						continue
					}
					methodNameCount[string(m.Name())]++
				}
			}
		}
		return true
	})
	return func(s protoreflect.ServiceDescriptor, m protoreflect.MethodDescriptor) string {
		hasShortCollision := simpleNameCount[string(s.Name())] > 1
		hasVeryShortCollision := methodNameCount[string(m.Name())] > 1
		return generateToolName(shortNames, veryShortNames, hasShortCollision, hasVeryShortCollision, string(s.FullName()), string(s.Name()), string(m.Name()))
	}
}

func buildRegistry(fds *descriptorpb.FileDescriptorSet) *protoregistry.Files {
	reg := new(protoregistry.Files)
	for _, f := range fds.GetFile() {
		fd, err := protodesc.NewFile(f, reg)
		if err != nil {
			continue
		}
		if _, err := reg.FindFileByPath(fd.Path()); err != nil {
			reg.RegisterFile(fd)
		}
	}
	return reg
}

func hasMethodOption(m protoreflect.MethodDescriptor, fieldNum uint32, expectedValues []uint64) bool {
	opts := m.Options()
	if opts == nil {
		return false
	}
	b, err := proto.Marshal(opts)
	if err != nil {
		return false
	}
	for len(b) > 0 {
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		if uint32(num) == fieldNum && wtype == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return false
			}
			return slices.Contains(expectedValues, v)
		}
		switch wtype {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(b)
		case protowire.Fixed32Type:
			_, n = protowire.ConsumeFixed32(b)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(b)
		case protowire.BytesType:
			_, n = protowire.ConsumeBytes(b)
		case protowire.StartGroupType:
			_, n = protowire.ConsumeGroup(protowire.Number(num), b)
		default:
			return false
		}
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}

func main() {
	headers := make(headerFlags)
	flag.Var(&headers, "header", "Headers to add to the backend request (Header: Value). Can apply multiple times.")
	serverName := flag.String("name", "gRPC MCP Server", "Name of MCP Server")
	serverVersion := flag.String("version", "1.0.0", "Version of MCP Server")
	sseHostPort := flag.String("hostport", "", "host:port for HTTP server, STDIN if not set")
	transport := flag.String("transport", "http", "Transport to use when hostport is set: http or sse")
	descriptors := flag.String("descriptors", "", "Location of the descriptor")
	reflect := flag.Bool("reflect", false, "Use reflection to get descriptors")
	services := flag.String("services", "", "If set, a comma separated list of services to expose")
	bearer := flag.String("bearer", "", "Token to use in an Authorization bearer header")
	bearerEnv := flag.String("bearer-env", "", "Environment variable for token to use in an Authorization bearer header")
	baseURL := flag.String("url", "http://localhost:8090", "The url of the backend")
	useConnect := flag.Bool("connect", false, "Use connect protocol (instead of gRPC)")
	requireMethodOption := flag.String("require-method-option", "", "Only expose methods with this option (fieldNumber:value or fieldNumber:value1,value2, e.g. 50003:1 or 50003:1,2)")
	shortNames := flag.Bool("short-names", false, "Use short tool names (ServiceName__MethodName instead of full package path). Saves tokens when used with LLM agents that list all tool names in context.")
	veryShortNames := flag.Bool("very-short-names", false, "Use very short tool names (MethodName only, no service prefix). Falls back to ServiceName__MethodName if method names collide across services, and to full path if service names also collide.")
	forwardOperatorIdentity := flag.Bool("forward-operator-identity", false, "Copy the X-Operator-Identity header from inbound MCP requests onto outbound gRPC calls. The header must be minted by a trusted proxy in front of this server; grpcmcp does not verify it.")
	string64 := flag.Bool("string64", false, "Expose 64-bit protobuf integer fields as strings only in JSON schemas")

	flag.Parse()

	var optFieldNum uint32
	var optValues []uint64
	if *requireMethodOption != "" {
		parts := strings.SplitN(*requireMethodOption, ":", 2)
		if len(parts) != 2 {
			fmt.Fprint(os.Stderr, "require-method-option must be in the format fieldNumber:value or fieldNumber:value1,value2\n")
			os.Exit(-1)
		}
		fn, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid field number in require-method-option: %v\n", err)
			os.Exit(-1)
		}
		for _, valStr := range strings.Split(parts[1], ",") {
			val, err := strconv.ParseUint(valStr, 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid value in require-method-option: %v\n", err)
				os.Exit(-1)
			}
			optValues = append(optValues, val)
		}
		optFieldNum = uint32(fn)
	}

	if *bearerEnv != "" {
		*bearer, _ = os.LookupEnv(*bearerEnv)
	}

	if *bearer != "" {
		http.Header(headers).Set("Authorization", "Bearer "+*bearer)
	}

	ctx := context.Background()

	if *descriptors == "" && !*reflect {
		fmt.Fprint(os.Stderr, "descriptors or reflect is required.\n")
		flag.Usage()
		os.Exit(-1)
	}

	descriptorSet, err := loadDescriptors(ctx, *descriptors, *reflect, *baseURL, http.Header(headers), *useConnect)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(-1)
	}
	serviceNames := parseServices(*services)
	servicesMap := map[string]struct{}{}
	for _, s := range serviceNames {
		servicesMap[string(s)] = struct{}{}
	}

	var methodFilter func(protoreflect.MethodDescriptor) bool
	if optFieldNum > 0 {
		methodFilter = func(m protoreflect.MethodDescriptor) bool {
			return hasMethodOption(m, optFieldNum, optValues)
		}
	}

	toolName := buildToolNamer(descriptorSet, servicesMap, methodFilter, *shortNames, *veryShortNames)

	headersProvider := grpcmcp.StaticHeaders(http.Header(headers))
	if *forwardOperatorIdentity {
		headersProvider = operatorIdentityHeaders(headersProvider)
	}

	cfg := grpcmcp.Config{
		Headers:       headersProvider,
		ServerName:    *serverName,
		Version:       *serverVersion,
		Descriptors:   descriptorSet,
		Services:      serviceNames,
		BaseURL:       *baseURL,
		UseConnect:    *useConnect,
		String64:      *string64,
		MethodFilter:  methodFilter,
		ToolName:      toolName,
		ServerOptions: []server.ServerOption{server.WithToolCapabilities(*reflect)},
	}
	srv, err := grpcmcp.NewServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(-1)
	}

	if *reflect {
		// Periodically re-run reflection so tools track the backend's schema:
		// new RPCs deployed on the backend appear without restarting grpcmcp.
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				newDescriptors, err := grpcmcp.LoadDescriptorsFromReflection(ctx, *baseURL, http.Header(headers), *useConnect)
				if err != nil {
					fmt.Fprintf(os.Stderr, "refresh: %v\n", err)
					continue
				}
				refreshCfg := cfg
				refreshCfg.Descriptors = newDescriptors
				refreshCfg.ToolName = buildToolNamer(newDescriptors, servicesMap, methodFilter, *shortNames, *veryShortNames)
				tools, err := grpcmcp.Tools(refreshCfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "refresh: %v\n", err)
					continue
				}
				srv.SetTools(tools...)
				fmt.Fprintf(os.Stderr, "refresh: %d tools registered\n", len(tools))
			}
		}()
	}

	if *sseHostPort == "" {
		if err := server.ServeStdio(srv); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		}
	} else {
		var err error
		switch *transport {
		case "http", "streamable-http":
			httpSrv := server.NewStreamableHTTPServer(srv)
			err = httpSrv.Start(*sseHostPort)
		case "sse":
			sseSrv := server.NewSSEServer(srv)
			err = sseSrv.Start(*sseHostPort)
		default:
			err = fmt.Errorf("unknown transport %q: expected http, streamable-http, or sse", *transport)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		}
	}
}

func loadDescriptors(ctx context.Context, descriptorsPath string, reflect bool, baseURL string, headers http.Header, useConnect bool) (*descriptorpb.FileDescriptorSet, error) {
	var descriptorSet *descriptorpb.FileDescriptorSet
	if reflect {
		var err error
		descriptorSet, err = grpcmcp.LoadDescriptorsFromReflection(ctx, baseURL, headers, useConnect)
		if err != nil {
			return nil, err
		}
	}
	if descriptorsPath != "" {
		return grpcmcp.LoadDescriptorsFromFile(descriptorsPath)
	}
	return descriptorSet, nil
}

func parseServices(services string) []protoreflect.FullName {
	if services == "" {
		return nil
	}
	parts := strings.Split(services, ",")
	result := make([]protoreflect.FullName, 0, len(parts))
	for _, service := range parts {
		service = strings.TrimSpace(service)
		if service != "" {
			result = append(result, protoreflect.FullName(service))
		}
	}
	return result
}
