package conformance

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireImage skips when docker or the image is unavailable, the same way the
// Python example-image smoke test does: these images are built by `make images`.
func requireImage(t *testing.T, image string) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("docker image %s not available: %v", image, err)
	}
}

func TestExtractManifestReadsTheExampleImage(t *testing.T) {
	requireImage(t, "msp-example-defect-cls:dev")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	raw, err := ExtractManifest(ctx, "msp-example-defect-cls:dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "name: defect-cls") {
		t.Fatalf("unexpected manifest:\n%s", raw)
	}
}

func TestExtractManifestMissingIsErrNoManifest(t *testing.T) {
	requireImage(t, "msp-base:dev") // the base image ships no model, hence no manifest
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := ExtractManifest(ctx, "msp-base:dev")
	if !errors.Is(err, ErrNoManifest) {
		t.Fatalf("want ErrNoManifest, got %v", err)
	}
}
