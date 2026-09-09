package main

import (
	"encoding/binary"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// echoServiceName is the RPC this example backend serves beyond the standard
// gRPC health/reflection services: a real round trip with a real argument and
// a real return value, for the end-to-end test to call through grpcmcp.
const echoServiceName = "echo.v1.EchoService"

// maxFrameLength bounds the length prefix on an inbound unary gRPC frame
// before allocating a buffer for it. Without a bound, a client-supplied
// 4-byte length (up to ~4GiB) drives an unbounded allocation from a single
// malformed request.
const maxFrameLength = 4 << 20 // 4MiB

// buildEchoFile constructs and registers the descriptor for echo.v1.EchoService.
// A real backend would get this from generated code; this one builds it by
// hand, the same way grpcmcp's own tests build descriptors, so this repo
// needs no protoc/buf codegen step.
func buildEchoFile() protoreflect.FileDescriptor {
	stringField := func(name string, number int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(number),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
		}
	}
	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("echo/v1/echo.proto"),
		Package: proto.String("echo.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  proto.String("EchoRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{stringField("message", 1)},
			},
			{
				Name:  proto.String("EchoResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{stringField("message", 1)},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("EchoService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Echo"),
						InputType:  proto.String(".echo.v1.EchoRequest"),
						OutputType: proto.String(".echo.v1.EchoResponse"),
					},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdProto, protoregistry.GlobalFiles)
	if err != nil {
		panic(err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		panic(err)
	}
	return fd
}

// echoHandler serves one unary gRPC method by hand: read the length-prefixed
// request frame, echo the "message" field back, write the length-prefixed
// response frame and the grpc-status trailer. No connect-go or generated
// stubs, since dynamicpb messages can't be plugged into connect's generic
// Handler API without a concrete Go type to instantiate.
func echoHandler(method protoreflect.MethodDescriptor) http.HandlerFunc {
	inputDesc := method.Input()
	outputDesc := method.Output()
	messageField := inputDesc.Fields().ByName("message")
	outMessageField := outputDesc.Fields().ByName("message")

	return func(w http.ResponseWriter, r *http.Request) {
		header := make([]byte, 5)
		if _, err := io.ReadFull(r.Body, header); err != nil {
			http.Error(w, "short frame header", http.StatusBadRequest)
			return
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length > maxFrameLength {
			http.Error(w, "frame too large", http.StatusRequestEntityTooLarge)
			return
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r.Body, payload); err != nil {
			http.Error(w, "short frame body", http.StatusBadRequest)
			return
		}

		req := dynamicpb.NewMessage(inputDesc)
		if err := proto.Unmarshal(payload, req); err != nil {
			http.Error(w, "bad request proto", http.StatusBadRequest)
			return
		}

		resp := dynamicpb.NewMessage(outputDesc)
		resp.Set(outMessageField, req.Get(messageField))

		respBytes, err := proto.Marshal(resp)
		if err != nil {
			http.Error(w, "marshal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		frame := make([]byte, 5+len(respBytes))
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(respBytes)))
		copy(frame[5:], respBytes)
		_, _ = w.Write(frame)
		w.Header().Set("Grpc-Status", "0")
	}
}
