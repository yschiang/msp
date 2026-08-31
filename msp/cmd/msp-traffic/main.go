// Command msp-traffic drives synthetic PredictRequest traffic at a
// ModelService target -- router-stub (MYSVC's predict round-trip stand-in)
// or a real model container -- and reports per-outcome counters plus latency
// percentiles (MSP-SPEC-001 §11 Phase 0). All behavior lives in
// internal/traffic; this file is flag parsing, wiring, summary printing, and
// the exit code only.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/yschiang/msp/msp/internal/traffic"
)

func main() {
	target := flag.String("target", "", "ModelService target, host:port (required)")
	model := flag.String("model", "", "model name sent in every PredictRequest (required)")
	n := flag.Int("n", 100, "total number of requests to send")
	concurrency := flag.Int("concurrency", 4, "number of concurrent workers")
	goldenDir := flag.String("golden-dir", "", "directory of sample-*/expected-* golden files to cycle payloads from; takes precedence over -random-payload-bytes")
	randomPayloadBytes := flag.Int("random-payload-bytes", 64, "random payload size in bytes, used when -golden-dir is unset")
	deviceCount := flag.Int("device-count", 10, "size of the round-robin device id pool (dev-000..dev-{N-1})")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "msp-traffic: -target is required")
		os.Exit(2)
	}
	if *model == "" {
		fmt.Fprintln(os.Stderr, "msp-traffic: -model is required")
		os.Exit(2)
	}

	cfg := traffic.Config{
		Target:             *target,
		Model:              *model,
		N:                  *n,
		Concurrency:        *concurrency,
		GoldenDir:          *goldenDir,
		RandomPayloadBytes: *randomPayloadBytes,
		DeviceCount:        *deviceCount,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res, err := traffic.Run(ctx, cfg)

	// Print the summary before checking err: a partial run (ctx cancelled
	// mid-flight) still has counts worth seeing, and those are exactly what
	// an operator watching an interrupted run wants -- not just the error.
	fmt.Printf("sent=%d ok=%d invalid_input=%d internal_error=%d transport_err=%d p50=%v p99=%v\n",
		res.Sent, res.OK, res.InvalidInput, res.InternalError, res.TransportErr, res.P50, res.P99)

	if err != nil {
		fmt.Fprintf(os.Stderr, "msp-traffic: %v\n", err)
		os.Exit(1)
	}
	if res.Failed(*goldenDir != "") {
		os.Exit(1)
	}
}
