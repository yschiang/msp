package conformance

// This file: conformance check C7 (resource declarations within platform
// ceilings, MSP-SPEC-001 §4.4). The ceilings are a Phase 0 stand-in for the
// spec's Q7 conformance-pool quota; `msp-conform verify --limits <yaml>`
// overrides them. Quantities use a minimal k8s-style parser (plain and "m"
// cpu, plain and Ki/Mi/Gi memory, integer gpu) — deliberately no k8s
// dependency.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Ceilings maps a resource name to its maximum allowed quantity, as the
// original declaration string (e.g. "8", "16Gi"). Kept as strings so error
// messages show the ceiling as the operator wrote it.
type Ceilings map[string]string

// DefaultCeilings is the Phase 0 stand-in for the platform quota (spec §1.5
// Q7): the conformance gate rejects any manifest asking for more than one
// node's worth of resources.
var DefaultCeilings = Ceilings{
	"cpu":            "8",
	"memory":         "16Gi",
	"nvidia.com/gpu": "1",
}

// LoadCeilings parses a --limits YAML file: a flat map from resource name to
// quantity, e.g.
//
//	cpu: "8"
//	memory: 16Gi
//	nvidia.com/gpu: "1"
//
// Keys present override DefaultCeilings; absent keys keep their defaults.
// Only resources with a default ceiling may appear — there is nothing to
// override otherwise.
func LoadCeilings(raw []byte) (Ceilings, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("limits file: %w", err)
	}
	c := Ceilings{}
	for k, v := range DefaultCeilings {
		c[k] = v
	}
	for k, v := range doc {
		if _, ok := DefaultCeilings[k]; !ok {
			return nil, fmt.Errorf("limits file: resource %q has no default ceiling in the reference build", k)
		}
		s := fmt.Sprint(v) // YAML scalars may arrive as int (cpu: 4) or string ("16Gi")
		if _, err := parseQuantity(k, s); err != nil {
			return nil, fmt.Errorf("limits file: %w", err)
		}
		c[k] = s
	}
	return c, nil
}

// CheckResources verifies every declared request and limit against the
// ceilings. A nil error is a C7 pass. Fail-closed: a resource name without a
// ceiling is an error, not a shrug — the gate cannot vouch for what it cannot
// bound.
func CheckResources(requests, limits map[string]string, c Ceilings) error {
	for _, section := range []struct {
		name string
		res  map[string]string
	}{{"requests", requests}, {"limits", limits}} {
		// Sort keys so the first error reported is deterministic.
		keys := make([]string, 0, len(section.res))
		for k := range section.res {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := section.res[k]
			ceilStr, ok := c[k]
			if !ok {
				return fmt.Errorf("%s %s: no ceiling defined for this resource in the reference build", section.name, k)
			}
			got, err := parseQuantity(k, v)
			if err != nil {
				return fmt.Errorf("%s %s: %w", section.name, k, err)
			}
			ceil, err := parseQuantity(k, ceilStr)
			if err != nil {
				return fmt.Errorf("ceiling for %s: %w", k, err)
			}
			if got > ceil {
				return fmt.Errorf("%s %s %q exceeds ceiling %q", section.name, k, v, ceilStr)
			}
		}
	}
	return nil
}

var (
	cpuPlainRe = regexp.MustCompile(`^\d+(\.\d+)?$`)
	intRe      = regexp.MustCompile(`^\d+$`)
)

// parseQuantity converts a resource quantity string to a canonical integer:
// millicores for cpu, bytes for memory, device count for gpu. Only the forms
// the spec's example manifests use are accepted ("2", "500m", "0.5" cpu;
// "1024", "4Gi" memory; "1" gpu) — anything else is an error naming the value.
func parseQuantity(resource, value string) (int64, error) {
	switch resource {
	case "cpu":
		if m, ok := strings.CutSuffix(value, "m"); ok && intRe.MatchString(m) {
			n, err := strconv.ParseInt(m, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("cpu quantity %q: %w", value, err)
			}
			return n, nil
		}
		if !cpuPlainRe.MatchString(value) {
			return 0, fmt.Errorf("cpu quantity %q: want <n>, <n.n>, or <n>m", value)
		}
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("cpu quantity %q: %w", value, err)
		}
		return int64(f * 1000), nil
	case "memory":
		mult := int64(1)
		num := value
		for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30} {
			if s, ok := strings.CutSuffix(value, suffix); ok {
				mult, num = m, s
				break
			}
		}
		if !intRe.MatchString(num) {
			return 0, fmt.Errorf("memory quantity %q: want <n>, <n>Ki, <n>Mi, or <n>Gi", value)
		}
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("memory quantity %q: %w", value, err)
		}
		return n * mult, nil
	case "nvidia.com/gpu":
		if !intRe.MatchString(value) {
			return 0, fmt.Errorf("gpu quantity %q: want a whole device count", value)
		}
		return strconv.ParseInt(value, 10, 64)
	default:
		return 0, fmt.Errorf("resource %q: no quantity syntax defined in the reference build", resource)
	}
}
