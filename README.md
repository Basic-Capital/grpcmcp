# grpcmcp

A simple MCP server that will proxy to a grpc backend based on a provided descriptors file or using reflection.

## Quick Start

1. Install the binary: `go install .` or `go install github.com/Basic-Capital/grpcmcp` Ensure the go bin directory is in your PATH.

2. In a terminal, run the example grpc server `go run example/main.go`. This will start a grpc health service on port 8090 with server reflection enabled. Note that this runs on the default port that grpcmcp will connect to.

3. **Streamable HTTP Transport** In another terminal, run `grpcmcp --hostport=localhost:3000 --reflect`. Specifying `hostport` will use Streamable HTTP by default. The MCP endpoint will be served at `http://localhost:3000/mcp`.

4. **Legacy SSE Transport** For older clients, run `grpcmcp --hostport=localhost:3000 --transport=sse --reflect`. The SSE endpoint will be served at `http://localhost:3000/sse`.

5. **STDIN Transport** Set up the MCP config. e.g.
```
"grpcmcp": {
    "command": "grpcmcp",
    "args": ["--reflect"]
}
```

## Options / Features

`grpcmcp --help` for a full list of options.

* `hostport` string - When set, serve MCP over HTTP, and use this as the server host:port.

* `transport` string - Transport to use when `hostport` is set. Defaults to `http` for Streamable HTTP at `/mcp`. Set to `sse` for the legacy SSE transport at `/sse`.

* `descriptors` string - Specify file location of the protobuf definitions generated from `buf build -o protos.pb` or `protoc --descriptor_set_out=protos.pb` instead of using gRPC reflection.

* `reflect` - If set, use reflection to retrieve gRPC endpoints instead of descriptor file.

* `url` string - Specify the url of the backend server.

* `services` string - Comma separated list of fully qualified gRPC service names to filter.

* `bearer` string - Token to attach in an `Authorization: Bearer` header.

* `bearer-env` string - Environment variable for token to attach in an `Authorization: Bearer` header. Overrides `bearer`.

* `header` string (repeatable) - Headers to add in `Key: Value` format.

* `short-names` - Use short tool names (`ServiceName__MethodName` instead of full package path). Falls back to the full path if two services share the same simple name.

* `very-short-names` - Use very short tool names (`MethodName` only, no service prefix). Falls back to `ServiceName__MethodName` if method names collide across services, and to the full path if service names also collide.

* `require-method-option` string - Only expose methods where a specific proto method option matches a given value. Format: `fieldNumber:value` (e.g. `50003:1`). This filters gRPC methods by checking the raw proto extension field on the method descriptor, so no generated code for the option is needed.

* `forward-operator-identity` - SSE mode only. Copy the `X-Operator-Identity` header from inbound requests onto outbound gRPC calls, so the backend can attribute agent calls to the human operator. The header must be minted by a trusted proxy in front of this server; grpcmcp does not verify it.

* `string64` - If set, expose 64-bit protobuf integer fields (`int64`, `uint64`, `sint64`, `fixed64`, `sfixed64`) as strings only in MCP JSON schemas. This avoids precision ambiguity for JavaScript-based clients and agents. By default, schemas continue to allow either JSON numbers or strings for compatibility.

* `refresh-interval` duration - How often to re-run reflection so methods added to the backend appear without a restart. Defaults to `5m`. This applies when `reflect` is set and `descriptors` is not: a descriptor file wins over reflection for the initial load, so a refresh from reflection would replace the set the operator asked for.

* `refresh-timeout` duration - Time limit for one refresh attempt. Defaults to `1m`. A backend that accepts the connection and then stalls would otherwise stop every later refresh.

A refresh that matches no tools leaves the current tool set in place. Reflection can report a reduced service set while a backend rolls, and taking every tool away from each connected client is worse than serving a set that is briefly stale.

### TLS

These options apply to the backend connection. They are ignored when `url` is `http://`.

* `client-ca-file` string - PEM roots used to verify the backend certificate.

* `client-tls-crt` string - Client certificate presented to the backend for mTLS. Set with `client-tls-key`.

* `client-tls-key` string - Key for the client certificate presented to the backend.

These options apply to the MCP server that grpcmcp runs when `hostport` is set. They work with both the `http` and the `sse` transport.

* `tls-crt` string - Certificate served by this server. Set with `tls-key` to serve TLS.

* `tls-key` string - Key for the certificate served by this server.

* `ca-file` string - PEM roots used to verify inbound client certificates. This requires every client to present one, so it turns on mTLS. Needs `tls-crt` and `tls-key`.

Reflection uses these options too, so `reflect` works against a backend that needs a custom CA or a client certificate.

grpcmcp currently exposes unary gRPC methods as MCP tools. Backend gRPC client-streaming, server-streaming, and bidi-streaming methods are skipped. This is separate from MCP transports: Streamable HTTP and legacy SSE are supported for client connections to grpcmcp.

## Library Usage

grpcmcp can also be embedded as a Go library. The library accepts a descriptor set directly, so applications can decide how to load descriptors and how to wrap the MCP HTTP handler.

```go
descriptors, err := grpcmcp.LoadDescriptorsFromReflection(ctx, backendURL, headers, false)
if err != nil {
    return err
}

srv, err := grpcmcp.NewServer(grpcmcp.Config{
    ServerName:  "gRPC MCP Server",
    Version:     "1.0.0",
    BaseURL:     backendURL,
    Descriptors: descriptors,
    Headers:     grpcmcp.StaticHeaders(headers),
})
if err != nil {
    return err
}

handler := server.NewStreamableHTTPServer(srv)
```

To route backend calls through a custom transport, provide `HTTPClient`. For example, an embedded application can use an in-memory HTTP transport such as `go.akshayshah.org/memhttp` by passing the in-memory server's URL and client:

```go
backend, err := memhttp.New(backendHandler)
if err != nil {
    return err
}
defer backend.Close()

srv, err := grpcmcp.NewServer(grpcmcp.Config{
    BaseURL:     backend.URL(),
    HTTPClient:  backend.Client(),
    Descriptors: descriptors,
})
```

For dynamic backend auth, provide a `ToolHeaderProvider`. The full `mcp.CallToolRequest` is available, including inbound HTTP headers supplied by supported MCP transports.

```go
srv, err := grpcmcp.NewServer(grpcmcp.Config{
    BaseURL:     backendURL,
    Descriptors: descriptors,
    Headers: func(ctx context.Context, req mcp.CallToolRequest) (http.Header, error) {
        h := make(http.Header)
        h.Set("Authorization", req.Header.Get("Authorization"))
        return h, nil
    },
})
```

Inbound MCP authentication should be handled by wrapping the HTTP handler with standard Go middleware. Outbound backend authentication is controlled by the configured header provider.

## Help

Join our Discord at https://discord.gg/hDjx3DehwG
