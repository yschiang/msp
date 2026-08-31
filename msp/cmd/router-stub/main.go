// Command router-stub serves a canned-response ModelService gRPC server: the
// Phase 0 integration target for MYSVC and the traffic generator before the
// real Router (Phase 2) exists (MSP-SPEC-001 §11 Phase 0). All behavior lives
// in internal/stub; this file is flag parsing and wiring only.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/stub"
	"google.golang.org/grpc"
)

func main() {
	port := flag.Int("port", 9090, "TCP port to listen on")
	responseFile := flag.String("response-file", "", "if set, every Predict response payload is this file's bytes instead of an echo of the request payload")
	flag.Parse()

	srv, err := stub.NewServer(*responseFile)
	if err != nil {
		log.Fatalf("router-stub: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("router-stub: listen on :%d: %v", *port, err)
	}

	gs := grpc.NewServer()
	servingv1.RegisterModelServiceServer(gs, srv)
	log.Printf("router-stub: listening on :%d", *port)
	if err := gs.Serve(lis); err != nil {
		log.Fatalf("router-stub: serve: %v", err)
	}
}
