// Package manifest loads and validates the model-manifest.yaml that every model
// image ships at /opt/msp/model-manifest.yaml (MSP-SPEC-001 §4.2). This is the
// core of conformance check C1.
package manifest

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

// schemaJSON is a copy of the canonical contract/manifest.schema.json, embedded
// so the binary needs no runtime file. `make sync-schema` refreshes it and
// TestSchemaCopyMatchesCanonical fails if the two ever diverge.
//
//go:embed manifest.schema.json
var schemaJSON string

// schema is compiled once; the embedded document is frozen, so a compile failure
// is a build defect and panicking is the right response.
var schema = jsonschema.MustCompileString("manifest.schema.json", schemaJSON)

type Manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Model      struct{ Name, Version, Description string }
	Contract   struct {
		Protocol     string
		Port         int
		InputSchema  SchemaRef `yaml:"inputSchema"`
		OutputSchema SchemaRef `yaml:"outputSchema"`
	}
	Runtime struct {
		Resources struct {
			Requests map[string]string
			Limits   map[string]string
		}
		StartupSeconds int `yaml:"startupSeconds"`
	}
	ComparisonPolicy string         `yaml:"comparisonPolicy"` // exact | numeric:<eps> | top-k:<k>
	GoldenSamples    []GoldenSample `yaml:"goldenSamples"`
}

// SchemaRef points at the payload schema for one direction of Predict.
// MessageType is set only when Type is "protobuf".
type SchemaRef struct {
	Type        string
	Descriptor  string
	MessageType string `yaml:"messageType"`
}

type GoldenSample struct{ Input, Output, Tolerance string }

// Load parses YAML, validates it against the embedded JSON Schema, and returns
// the typed manifest. Validation errors name the offending instance path.
func Load(raw []byte) (*Manifest, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("model-manifest.yaml: %w", err)
	}

	// Round-trip through JSON so the document is made of the types the
	// validator understands (and so anything YAML allows but JSON does not,
	// such as non-string mapping keys, is rejected here with a clear error).
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("model-manifest.yaml is not representable as JSON: %w", err)
	}
	var jsonDoc any
	if err := json.Unmarshal(buf, &jsonDoc); err != nil {
		return nil, fmt.Errorf("model-manifest.yaml: %w", err)
	}

	if err := schema.Validate(jsonDoc); err != nil {
		return nil, fmt.Errorf("model-manifest.yaml is invalid: %s", describe(err))
	}

	// Deliberately a second, independent decode of the original bytes -- not a
	// conversion of the tree that was just validated. The two can differ where
	// yaml.v3 resolves implicit tags: `version: 2026-08-01` validates as the
	// normalized string "2026-08-01T00:00:00Z" but lands here as the raw text.
	// No field in spec §4.2 is date-shaped, so this is a note, not a defect.
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("model-manifest.yaml: %w", err)
	}
	return &m, nil
}

// describe flattens a jsonschema validation error into one deterministic line of
// "<instance path>: <reason>" entries -- the whole point being that the caller
// sees which field is wrong, not just that something is.
func describe(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}

	var msgs []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := e.InstanceLocation
			if loc == "" {
				loc = "/"
			}
			msgs = append(msgs, loc+": "+e.Message)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)

	// Causes follow map iteration order, so sort for a stable message.
	slices.Sort(msgs)
	return strings.Join(slices.Compact(msgs), "; ")
}
