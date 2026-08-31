# grpcmcp

A simple MCP server that will proxy to a grpc backend based on a provided descriptors file or using reflection.

## Quick Start

1. Install the binary: `go install .` or `go install github.com/Basic-Capital/grpcmcp` Ensure the go bin directory is in your PATH.

2. In a terminal, run the example grpc server `go run example/main.go`. This will start a grpc health service on port 8090 with server reflection enabled. Note that this runs on the default port that grpcmcp will connect to.

3. **Streamable HTTP** In another terminal, run `grpcmcp --hostport=localhost:3000 --reflect`. The MCP endpoint is served at `http://localhost:3000/mcp`.

4. **stdio** Set up the MCP config. e.g.
```
"grpcmcp": {
    "command": "grpcmcp",
    "args": ["--reflect"]
}
```

## Transports

grpcmcp serves one of three transports, chosen by `hostport` and `transport`:

* Without `hostport`, it serves **stdio**.
* With `hostport`, it serves **Streamable HTTP** at `/mcp` (default, `transport=http`).
* With `hostport` and `transport=sse`, it serves the legacy, stateful **SSE** transport instead. This is deprecated as of MCP 2026-07-28 and kept only for clients that cannot yet speak Streamable HTTP; prefer the default.

The HTTP server is stateless. It mints no session, it ignores an inbound
`Mcp-Session-Id`, and it answers `GET /mcp` with 405, because it opens no
server-to-client stream. Every tool call is an independent unary gRPC request,
so any replica can serve any request and a restart costs a client nothing.

One consequence: over HTTP, grpcmcp does not declare the `listChanged` tool
capability and sends no `notifications/tools/list_changed`. A stateless server
holds no client to notify. A refresh still replaces the tool set, and a client
sees the new set when it next calls `tools/list`. Over stdio the capability is
declared and the notification is sent.

grpcmcp validates the `Origin` header and answers 403 when one is present. Only
a browser sets `Origin`, and this server has no browser client, so the check
needs no origin list. This is the MCP requirement that guards against DNS
rebinding.

grpcmcp also answers 400 when a request names an `Mcp-Protocol-Version` it does
not implement, and the error body names the versions it does support. A request
that omits the header is accepted and read as `2025-03-26`, which the spec
allows for clients older than `2025-06-18`.

## Options / Features

`grpcmcp --help` for a full list of options.

* `hostport` string - When set, serve HTTP on this host:port. Without it, serve stdio.
* `transport` string - Which HTTP transport to serve when `hostport` is set: `http` (default, stateless Streamable HTTP at `/mcp`) or `sse` (deprecated legacy transport, kept for clients that cannot yet speak Streamable HTTP).

* `instructions` string - Natural-language guidance for the agent on what this server is for. Sent to the client during initialization.

* `descriptors` string - Specify file location of the protobuf definitions generated from `buf build -o protos.pb` or `protoc --descriptor_set_out=protos.pb` instead of using gRPC reflection.

* `reflect` - If set, use reflection to retrieve gRPC endpoints instead of descriptor file.

* `url` string - Specify the url of the backend server.

* `services` string - Comma separated list of fully qualified gRPC service names to filter.

* `bearer-env` string - Environment variable holding a token to attach in an `Authorization: Bearer` header on backend calls. The token is read from the environment and never from a flag, because an argv value is visible to every process on the host through `ps`. grpcmcp exits when the named variable is empty or unset.

* `header` string (repeatable) - Headers to add in `Key: Value` format.

* `require-method-option` string - Only expose methods where a specific proto method option matches a given value. Format: `fieldNumber:value` (e.g. `50003:1`). This filters gRPC methods by checking the raw proto extension field on the method descriptor, so no generated code for the option is needed.

* `forward-operator-identity` - Copy the `X-Operator-Identity` header from inbound MCP requests onto outbound gRPC calls, so the backend can attribute agent calls to the human operator. The header is read per request, so it identifies the caller of that tool call. It must be minted by a trusted proxy in front of this server; grpcmcp does not verify it. This needs `hostport`, because stdio carries no HTTP headers.

* `string64` - If set, expose 64-bit protobuf integer fields (`int64`, `uint64`, `sint64`, `fixed64`, `sfixed64`) as strings only in MCP JSON schemas. This avoids precision ambiguity for JavaScript-based clients and agents. By default, schemas continue to allow either JSON numbers or strings for compatibility.

* `refresh-interval` duration - How often to re-run reflection so methods added to the backend appear without a restart. Defaults to `5m`. This applies when `reflect` is set and `descriptors` is not: a descriptor file wins over reflection for the initial load, so a refresh from reflection would replace the set the operator asked for.

* `refresh-timeout` duration - Time limit for one refresh attempt. Defaults to `1m`. A backend that accepts the connection and then stalls would otherwise stop every later refresh.

A refresh that matches no tools leaves the current tool set in place. Reflection can report a reduced service set while a backend rolls, and taking every tool away from each connected client is worse than serving a set that is briefly stale.

### Tool names

grpcmcp gives each tool the shortest name that stays unique, because an agent
holds every tool name in its context:

1. The method name on its own, such as `GetPlan`.
2. `ServiceName__MethodName`, when another exposed service defines the same method name.
3. The full package path with dots replaced by underscores, such as `com_example_wallet_WalletService__GetPlan`, when two services also share a simple name.

The scan that finds these collisions counts only the methods grpcmcp exposes, so
`services` and `require-method-option` change which names collide.

### TLS

These options apply to the backend connection. They are ignored when `url` is `http://`.

* `client-ca-file` string - PEM roots used to verify the backend certificate.

* `client-tls-crt` string - Client certificate presented to the backend for mTLS. Set with `client-tls-key`.

* `client-tls-key` string - Key for the client certificate presented to the backend.

These options apply to the MCP server that grpcmcp runs when `hostport` is set.

* `tls-crt` string - Certificate served by this server. Set with `tls-key` to serve TLS.

* `tls-key` string - Key for the certificate served by this server.

* `ca-file` string - PEM roots used to verify inbound client certificates. This requires every client to present one, so it turns on mTLS. Needs `tls-crt` and `tls-key`.

Reflection uses these options too, so `reflect` works against a backend that needs a custom CA or a client certificate.

grpcmcp exposes unary gRPC methods as MCP tools. Backend gRPC client-streaming, server-streaming, and bidi-streaming methods are skipped.

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

For dynamic backend auth, provide a `ToolHeaderProvider`. The full `mcp.CallToolRequest` is available, including the inbound HTTP headers of that request when grpcmcp serves Streamable HTTP. Over stdio there are no inbound headers.

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
