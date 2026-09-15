package syncctl

// This file: the two registries and the digest work between them (design D8):
// resolve a tag at model-center, copy the manifest byte-for-byte to
// platform-internal, read the digest back. go-containerregistry as a
// library — no CLI to install, and unlike docker pull/tag/push it never
// rewrites a manifest (which would change the digest silently).

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/crane"
)

// Registries holds host:port of the model-center and platform-internal
// registries. Both are plain http (crane.Insecure; localhost is http by
// default anyway).
type Registries struct {
	ModelCenter string
	Internal    string
}

func (r Registries) SourceRef(model, version string) string {
	return r.ModelCenter + "/" + model + ":" + version
}
func (r Registries) PinnedSourceRef(model, digest string) string {
	return r.ModelCenter + "/" + model + "@" + digest
}
func (r Registries) InternalRef(model, digest string) string {
	return r.Internal + "/" + model + "@" + digest
}

func craneOpts(ctx context.Context) []crane.Option {
	return []crane.Option{crane.Insecure, crane.WithContext(ctx)}
}

// ResolveDigest returns the manifest digest a reference currently points at.
func ResolveDigest(ctx context.Context, ref string) (string, error) {
	d, err := crane.Digest(ref, craneOpts(ctx)...)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	return d, nil
}

// CopyImage copies src (image or index) to dst and returns the digest dst
// reports back. It does not judge: the caller compares it to the pinned
// digest and rejects loudly on a mismatch (D8 step 5).
func CopyImage(ctx context.Context, src, dst string) (string, error) {
	if err := crane.Copy(src, dst, craneOpts(ctx)...); err != nil {
		return "", fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return ResolveDigest(ctx, dst)
}
