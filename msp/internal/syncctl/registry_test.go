package syncctl

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
)

func TestRefs(t *testing.T) {
	r := Registries{ModelCenter: "localhost:5010", Internal: "localhost:5011"}
	if got := r.SourceRef("defect-cls", "v1"); got != "localhost:5010/defect-cls:v1" {
		t.Error(got)
	}
	if got := r.PinnedSourceRef("defect-cls", goodDigest); got != "localhost:5010/defect-cls@"+goodDigest {
		t.Error(got)
	}
	if got := r.InternalRef("defect-cls", goodDigest); got != "localhost:5011/defect-cls@"+goodDigest {
		t.Error(got)
	}
}

// Two in-memory registries stand in for model-center and platform-internal.
// The point of the test is D8: the digest read back from the destination is
// the digest that was resolved at the source.
func TestResolveAndCopyPreserveDigest(t *testing.T) {
	src := httptest.NewServer(registry.New())
	defer src.Close()
	dst := httptest.NewServer(registry.New())
	defer dst.Close()
	regs := Registries{
		ModelCenter: strings.TrimPrefix(src.URL, "http://"),
		Internal:    strings.TrimPrefix(dst.URL, "http://"),
	}

	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := crane.Push(img, regs.SourceRef("defect-cls", "v1"), crane.Insecure); err != nil {
		t.Fatal(err)
	}
	want, _ := img.Digest()

	ctx := context.Background()
	got, err := ResolveDigest(ctx, regs.SourceRef("defect-cls", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("resolved %s, want %s", got, want)
	}

	back, err := CopyImage(ctx, regs.PinnedSourceRef("defect-cls", got), regs.InternalRef("defect-cls", got))
	if err != nil {
		t.Fatal(err)
	}
	if back != got {
		t.Fatalf("internal registry reports %s, pinned %s", back, got)
	}
}

func TestResolveDigestUnreachableIsAnError(t *testing.T) {
	if _, err := ResolveDigest(context.Background(), "127.0.0.1:1/defect-cls:v1"); err == nil {
		t.Fatal("want error for unreachable registry")
	}
}
