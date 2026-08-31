package conformance

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReportJSONRoundTrip(t *testing.T) {
	r := Report{
		Image: "msp-example-defect-cls:dev",
		Checks: []CheckResult{
			{ID: "C1", Name: "manifest", Pass: true, Detail: "manifest parsed and valid"},
			{ID: "C2", Name: "startup", Pass: false, Detail: "not ready within 10s"},
		},
		Pass: false,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Task 9 asserts on check IDs in the JSON, so the key names are contract.
	for _, key := range []string{`"Image"`, `"Checks"`, `"Pass"`, `"ID"`, `"Name"`, `"Detail"`, `"C1"`, `"C2"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("JSON %s missing %s", b, key)
		}
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("round trip: got %+v, want %+v", got, r)
	}
}

func TestReportAggregate(t *testing.T) {
	tests := []struct {
		name   string
		checks []CheckResult
		want   bool
	}{
		{"all pass", []CheckResult{{ID: "C1", Pass: true}, {ID: "C2", Pass: true}}, true},
		{"one fail", []CheckResult{{ID: "C1", Pass: true}, {ID: "C2", Pass: false}}, false},
		{"empty is not a pass", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Report{Checks: tt.checks}
			r.Aggregate()
			if r.Pass != tt.want {
				t.Errorf("Pass = %v, want %v", r.Pass, tt.want)
			}
		})
	}
}

func TestReportMissingChecks(t *testing.T) {
	r := Report{Checks: []CheckResult{{ID: "C1", Pass: false}, {ID: "C2", Pass: true}, {ID: "C7", Pass: true}}}
	got := r.MissingChecks()
	want := []string{"C3", "C4", "C5", "C6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingChecks = %v, want %v", got, want)
	}
	full := Report{}
	for _, id := range AllCheckIDs {
		full.Checks = append(full.Checks, CheckResult{ID: id, Pass: true})
	}
	if missing := full.MissingChecks(); len(missing) != 0 {
		t.Errorf("MissingChecks on full report = %v, want none", missing)
	}
}

func TestWriteHuman(t *testing.T) {
	r := Report{
		Image: "img:dev",
		Checks: []CheckResult{
			{ID: "C1", Name: "manifest", Pass: true, Detail: "manifest parsed and valid"},
			{ID: "C2", Name: "startup", Pass: false, Detail: "not ready within 10s"},
		},
	}
	r.Aggregate()
	var sb strings.Builder
	r.WriteHuman(&sb)
	out := sb.String()
	for _, want := range []string{"C1", "PASS", "C2", "FAIL", "not ready within 10s", "not run: C3, C4, C5, C6, C7", "FAIL: img:dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}

	full := Report{Image: "img:dev"}
	for _, id := range AllCheckIDs {
		full.Checks = append(full.Checks, CheckResult{ID: id, Name: CheckNames[id], Pass: true, Detail: "ok"})
	}
	full.Aggregate()
	sb.Reset()
	full.WriteHuman(&sb)
	if !strings.Contains(sb.String(), "PASS: img:dev") {
		t.Errorf("human output missing overall PASS:\n%s", sb.String())
	}
	if strings.Contains(sb.String(), "not run") {
		t.Errorf("full report should not list not-run checks:\n%s", sb.String())
	}
}
