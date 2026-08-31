package conformance

import (
	"strings"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		resource, value string
		want            int64
		wantErr         bool
	}{
		{"cpu", "2", 2000, false},
		{"cpu", "500m", 500, false},
		{"cpu", "0.5", 500, false},
		{"cpu", "8", 8000, false},
		{"cpu", "abc", 0, true},
		{"cpu", "-1", 0, true},
		{"cpu", "", 0, true},
		{"memory", "4Gi", 4 << 30, false},
		{"memory", "512Mi", 512 << 20, false},
		{"memory", "16Ki", 16 << 10, false},
		{"memory", "1024", 1024, false},
		{"memory", "1G", 0, true}, // decimal suffixes are not in the reference build
		{"memory", "Gi", 0, true},
		{"nvidia.com/gpu", "1", 1, false},
		{"nvidia.com/gpu", "0.5", 0, true}, // GPUs are whole devices
		{"ephemeral-storage", "1Gi", 0, true},
	}
	for _, tt := range tests {
		got, err := parseQuantity(tt.resource, tt.value)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseQuantity(%q, %q) = %d, want error", tt.resource, tt.value, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseQuantity(%q, %q): %v", tt.resource, tt.value, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseQuantity(%q, %q) = %d, want %d", tt.resource, tt.value, got, tt.want)
		}
	}
}

func TestCheckResources(t *testing.T) {
	tests := []struct {
		name              string
		requests, limits  map[string]string
		wantErrContaining string // empty = pass
	}{
		{
			name:     "within ceilings passes",
			requests: map[string]string{"cpu": "1", "memory": "1Gi"},
			limits:   map[string]string{"cpu": "2", "memory": "2Gi"},
		},
		{
			name:              "cpu 64 exceeds ceiling 8",
			limits:            map[string]string{"cpu": "64"},
			wantErrContaining: `limits cpu "64" exceeds ceiling "8"`,
		},
		{
			name:              "gpu 2 exceeds ceiling 1",
			requests:          map[string]string{"nvidia.com/gpu": "2"},
			wantErrContaining: `requests nvidia.com/gpu "2" exceeds ceiling "1"`,
		},
		{
			name:              "memory 32Gi exceeds ceiling 16Gi",
			limits:            map[string]string{"memory": "32Gi"},
			wantErrContaining: `limits memory "32Gi" exceeds ceiling "16Gi"`,
		},
		{
			name:              "unknown resource fails closed",
			requests:          map[string]string{"hugepages-2Mi": "1Gi"},
			wantErrContaining: "no ceiling",
		},
		{
			name:              "unparseable value",
			limits:            map[string]string{"cpu": "lots"},
			wantErrContaining: `"lots"`,
		},
		{
			name: "empty declarations pass",
		},
		{
			name:     "at the ceiling passes",
			requests: map[string]string{"cpu": "8", "memory": "16Gi", "nvidia.com/gpu": "1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckResources(tt.requests, tt.limits, DefaultCeilings)
			if tt.wantErrContaining == "" {
				if err != nil {
					t.Fatalf("CheckResources: %v, want pass", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckResources passed, want error containing %q", tt.wantErrContaining)
			}
			if !strings.Contains(err.Error(), tt.wantErrContaining) {
				t.Errorf("error %q does not contain %q", err, tt.wantErrContaining)
			}
		})
	}
}

func TestLoadCeilings(t *testing.T) {
	c, err := LoadCeilings([]byte("memory: 8Gi\nnvidia.com/gpu: \"0\"\n"))
	if err != nil {
		t.Fatalf("LoadCeilings: %v", err)
	}
	// Overridden keys take effect; absent keys keep the defaults.
	if err := CheckResources(nil, map[string]string{"memory": "16Gi"}, c); err == nil {
		t.Error("memory 16Gi should exceed overridden ceiling 8Gi")
	}
	if err := CheckResources(nil, map[string]string{"nvidia.com/gpu": "1"}, c); err == nil {
		t.Error("gpu 1 should exceed overridden ceiling 0")
	}
	if err := CheckResources(nil, map[string]string{"cpu": "8"}, c); err != nil {
		t.Errorf("cpu 8 should still pass under default ceiling: %v", err)
	}

	// Unquoted YAML integers must work too.
	if _, err := LoadCeilings([]byte("cpu: 4\n")); err != nil {
		t.Errorf("LoadCeilings with unquoted int: %v", err)
	}

	if _, err := LoadCeilings([]byte("disk: 1Gi\n")); err == nil {
		t.Error("LoadCeilings should reject a resource with no default ceiling")
	}
	if _, err := LoadCeilings([]byte(":::")); err == nil {
		t.Error("LoadCeilings should reject invalid YAML")
	}
}
