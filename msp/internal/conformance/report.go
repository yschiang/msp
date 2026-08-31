// Package conformance implements msp-conform: the C1–C7 gate a model image
// must pass before it is deployable (MSP-SPEC-001 §4.4). This file holds the
// report types shared by `verify` (host side) and `probe` (netns side) and
// their rendering. Check IDs are stable strings — Task 9's negative fixtures
// assert on them in the JSON output.
package conformance

import (
	"fmt"
	"io"
	"strings"
)

// CheckResult is one conformance check's verdict. A check appears in a Report
// only if it ran to a verdict; a check blocked by an earlier failure is simply
// absent (see Report.MissingChecks).
type CheckResult struct {
	ID, Name string
	Pass     bool
	Detail   string
}

// Report is the machine-readable output of a verify or probe run.
type Report struct {
	Image  string
	Checks []CheckResult
	Pass   bool
}

// AllCheckIDs lists the seven conformance checks in spec order. A verify run
// passes only when all seven are present and passing.
var AllCheckIDs = []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}

// CheckNames maps check IDs to the short names printed in the human table
// (after the spec §4.4 table).
var CheckNames = map[string]string{
	"C1": "manifest",
	"C2": "offline-startup",
	"C3": "envelope",
	"C4": "schema",
	"C5": "golden-samples",
	"C6": "idempotency",
	"C7": "resources",
}

// Aggregate sets Pass: every present check passed and at least one check ran.
// It says nothing about completeness — callers that require all seven checks
// must also consult MissingChecks.
func (r *Report) Aggregate() {
	r.Pass = len(r.Checks) > 0
	for _, c := range r.Checks {
		if !c.Pass {
			r.Pass = false
			return
		}
	}
}

// MissingChecks returns the IDs from AllCheckIDs absent from the report, in
// spec order — the checks that never ran because a prerequisite failed.
func (r *Report) MissingChecks() []string {
	present := map[string]bool{}
	for _, c := range r.Checks {
		present[c.ID] = true
	}
	var missing []string
	for _, id := range AllCheckIDs {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// WriteHuman renders the per-check PASS/FAIL table, the not-run list, and the
// overall verdict.
func (r *Report) WriteHuman(w io.Writer) {
	for _, c := range r.Checks {
		verdict := "PASS"
		if !c.Pass {
			verdict = "FAIL"
		}
		// Indent continuation lines of multi-line details (e.g. log tails)
		// so the table stays scannable.
		detail := strings.ReplaceAll(c.Detail, "\n", "\n      ")
		fmt.Fprintf(w, "%-3s %-16s %s  %s\n", c.ID, c.Name, verdict, detail)
	}
	if missing := r.MissingChecks(); len(missing) > 0 {
		fmt.Fprintf(w, "not run: %s (blocked by a failed prerequisite check)\n", strings.Join(missing, ", "))
	}
	// Trust r.Pass: VerifyImage folds not-run checks into it, and the probe's
	// five-check report legitimately passes with C1/C7 absent (host-side).
	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	fmt.Fprintf(w, "%s: %s\n", verdict, r.Image)
}
