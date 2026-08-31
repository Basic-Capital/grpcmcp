package main

import (
	"encoding/binary"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// buildEchoFile registers echo.v1's descriptor in the global proto registry,
// which panics on a second registration. Every test needs the descriptor, so
// build it once and share it, rather than once per test.
var echoMethodOnce = sync.OnceValue(func() protoreflect.MethodDescriptor {
	return buildEchoFile().Services().Get(0).Methods().Get(0)
})

func newEchoMethod(t *testing.T) protoreflect.MethodDescriptor {
	t.Helper()
	return echoMethodOnce()
}

// TestEchoOversizedFrameRejected checks that a claimed frame length over
// maxFrameLength is rejected before the handler allocates a buffer for it,
// rather than trusting a client-controlled 4-byte length unconditionally.
func TestEchoOversizedFrameRejected(t *testing.T) {
	handler := echoHandler(newEchoMethod(t))

	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:], maxFrameLength+1)
	// No payload bytes follow -- if the length check did not run first, the
	// handler would instead fail on the short read with a different status.
	req := httptest.NewRequest("POST", "/echo.v1.EchoService/Echo", strings.NewReader(string(header)))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != 413 {
		t.Errorf("status = %d, want 413 (Request Entity Too Large)", rec.Code)
	}
}

// TestEchoRoundTrip checks the handler still works normally for a real,
// small request -- the oversized-frame guard must not reject legitimate
// calls.
func TestEchoRoundTrip(t *testing.T) {
	method := newEchoMethod(t)
	handler := echoHandler(method)

	req := dynamicpb.NewMessage(method.Input())
	req.Set(method.Input().Fields().ByName("message"), protoreflect.ValueOfString("hi"))
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)

	httpReq := httptest.NewRequest("POST", "/echo.v1.EchoService/Echo", strings.NewReader(string(frame)))
	rec := httptest.NewRecorder()
	handler(rec, httpReq)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if len(body) < 5 {
		t.Fatalf("response too short: %d bytes", len(body))
	}
	resp := dynamicpb.NewMessage(method.Output())
	if err := proto.Unmarshal(body[5:], resp); err != nil {
		t.Fatal(err)
	}
	if got := resp.Get(method.Output().Fields().ByName("message")).String(); got != "hi" {
		t.Errorf("message = %q, want %q", got, "hi")
	}
}
