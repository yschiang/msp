package conformance

// docker.go's orchestration is exercised end-to-end by Task 9's integration
// fixtures; only its pure merge logic is unit-tested here.

import (
	"strings"
	"testing"
)

func TestMergeC4(t *testing.T) {
	staticPass := CheckResult{ID: "C4", Name: "schema", Pass: true, Detail: "static ok"}
	staticFail := CheckResult{ID: "C4", Name: "schema", Pass: false, Detail: "jsonschema: not implemented in reference build"}
	livePass := CheckResult{ID: "C4", Name: "schema", Pass: true, Detail: "live ok"}
	liveFail := CheckResult{ID: "C4", Name: "schema", Pass: false, Detail: "unknown fields"}

	if got := mergeC4(staticPass, &livePass); got == nil || !got.Pass {
		t.Errorf("static pass + live pass = %+v, want pass", got)
	} else if !strings.Contains(got.Detail, "static ok") || !strings.Contains(got.Detail, "live ok") {
		t.Errorf("merged detail %q should carry both halves", got.Detail)
	}
	if got := mergeC4(staticPass, &liveFail); got == nil || got.Pass {
		t.Errorf("static pass + live fail = %+v, want fail", got)
	}
	if got := mergeC4(staticFail, &livePass); got == nil || got.Pass {
		t.Errorf("static fail + live pass = %+v, want fail", got)
	} else if !strings.Contains(got.Detail, "not implemented") {
		t.Errorf("merged detail %q should carry the static failure", got.Detail)
	}
	if got := mergeC4(staticFail, nil); got == nil || got.Pass {
		t.Errorf("static fail + live absent = %+v, want fail", got)
	}
	// Static pass alone does not fully verify C4: the check is not-run.
	if got := mergeC4(staticPass, nil); got != nil {
		t.Errorf("static pass + live absent = %+v, want nil (not run)", got)
	}
}
