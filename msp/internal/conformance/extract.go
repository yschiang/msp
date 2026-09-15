package conformance

// This file: reading one file out of a model image without running it — the
// docker create + cp path VerifyImage also uses, exposed on its own so the
// Sync Controller can read /opt/msp/model-manifest.yaml (Phase 1 D13).
// VerifyImage keeps its own combined extraction (manifest, goldens and
// descriptors from one container); this shares only dockerRun with it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestPath is the frozen in-image path of the model manifest.
const ManifestPath = "/opt/msp/model-manifest.yaml"

// ErrNoManifest reports an image with no manifest at ManifestPath. Callers
// that must tell "bad image" from "docker is broken" match it with errors.Is;
// every other error from ExtractManifest is infrastructure.
var ErrNoManifest = errors.New(ManifestPath + " not found in image")

// ExtractManifest returns the raw bytes of the image's model manifest. The
// image is pulled if missing (docker create pulls), never run.
func ExtractManifest(ctx context.Context, image string) ([]byte, error) {
	out, err := dockerRun(ctx, "create", image)
	if err != nil {
		return nil, fmt.Errorf("docker create %s: %w", image, err)
	}
	id := strings.TrimSpace(out)
	// Fresh context: ctx may be cancelled by the time we get here.
	defer dockerRun(context.Background(), "rm", "-f", id) //nolint:errcheck

	dir, err := os.MkdirTemp("", "msp-extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "model-manifest.yaml")
	// Same attribution VerifyImage makes for C1: create succeeded a moment
	// ago, so a failing cp means the file is absent, not that docker is down.
	if _, err := dockerRun(ctx, "cp", id+":"+ManifestPath, dst); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoManifest, err)
	}
	return os.ReadFile(dst)
}
