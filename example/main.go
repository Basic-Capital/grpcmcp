package main

import (
	"net/http"

	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	mux := http.NewServeMux()
	health := grpchealth.NewStaticChecker(grpchealth.HealthV1ServiceName)
	reflector := grpcreflect.NewStaticReflector(grpchealth.HealthV1ServiceName, echoServiceName)
	mux.Handle(grpchealth.NewHandler(health))
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	echoFile := buildEchoFile()
	echoMethod := echoFile.Services().Get(0).Methods().Get(0)
	mux.HandleFunc("/"+echoServiceName+"/Echo", echoHandler(echoMethod))

	if err := http.ListenAndServe("localhost:8090", h2c.NewHandler(mux, &http2.Server{})); err != nil {
		panic(err)
	}
}
