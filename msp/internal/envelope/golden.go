package envelope

// This file: golden sample loading and output comparison for conformance
// checks C5 (golden outputs match expected) and C6 (idempotency), spec §4.4.

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yschiang/msp/msp/internal/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// GoldenPair is one golden sample with its effective tolerance (the sample's
// own tolerance, or the manifest's comparisonPolicy when the sample declares
// none).
type GoldenPair struct {
	Input, Expected []byte
	Tolerance       string
}

// LoadGoldens reads every golden sample declared in the manifest. Manifest
// paths are absolute container paths (/opt/msp/golden/sample-01.bin); dir is a
// host directory holding the extracted files under their container basenames,
// so each path resolves to filepath.Join(dir, filepath.Base(path)).
func LoadGoldens(m *manifest.Manifest, dir string) ([]GoldenPair, error) {
	pairs := make([]GoldenPair, 0, len(m.GoldenSamples))
	for i, gs := range m.GoldenSamples {
		in, err := os.ReadFile(filepath.Join(dir, filepath.Base(gs.Input)))
		if err != nil {
			return nil, fmt.Errorf("golden sample %d input: %w", i, err)
		}
		out, err := os.ReadFile(filepath.Join(dir, filepath.Base(gs.Output)))
		if err != nil {
			return nil, fmt.Errorf("golden sample %d output: %w", i, err)
		}
		tol := gs.Tolerance
		if tol == "" {
			tol = m.ComparisonPolicy
		}
		pairs = append(pairs, GoldenPair{Input: in, Expected: out, Tolerance: tol})
	}
	return pairs, nil
}

// Compare checks actual against expected under the given tolerance.
//
//   - "exact": byte equality; the descriptor arguments are never touched.
//   - "numeric:<eps>": decode both payloads with the manifest's output
//     descriptor (out.Descriptor is an absolute container path, resolved to
//     filepath.Join(descDir, filepath.Base(...))) and compare float/double
//     fields within eps; everything else — including presence, map keys,
//     list lengths, and unknown fields — must match exactly.
//   - "top-k:<k>": accepted by the manifest schema but not implemented here.
//
// A nil return means the outputs match; any difference returns an error
// naming the field path and both values.
//
// ponytail: the descriptor set is read from disk on every numeric call. Phase
// 0 runs a handful of goldens per model against a 266-byte descriptor; add a
// cache keyed by (descDir, basename) if conformance ever runs thousands.
func Compare(expected, actual []byte, tolerance string, out manifest.SchemaRef, descDir string) error {
	switch {
	case tolerance == "exact":
		return compareExact(expected, actual)
	case strings.HasPrefix(tolerance, "numeric:"):
		eps, err := strconv.ParseFloat(strings.TrimPrefix(tolerance, "numeric:"), 64)
		if err != nil || eps < 0 {
			return fmt.Errorf("tolerance %q: epsilon must be a non-negative number", tolerance)
		}
		return compareNumeric(expected, actual, eps, out, descDir)
	case strings.HasPrefix(tolerance, "top-k:"):
		return fmt.Errorf("tolerance %q: not implemented in reference build", tolerance)
	default:
		return fmt.Errorf("unknown tolerance %q", tolerance)
	}
}

func compareExact(expected, actual []byte) error {
	if bytes.Equal(expected, actual) {
		return nil
	}
	i := 0
	for i < len(expected) && i < len(actual) && expected[i] == actual[i] {
		i++
	}
	return fmt.Errorf("payloads differ at byte %d (expected %d bytes, actual %d bytes)",
		i, len(expected), len(actual))
}

// LoadMessageDescriptor resolves ref to the message descriptor it names. The
// declared path is a container path; the file is read from descDir under its
// basename, which is how image extraction lays descriptors out. Only protobuf
// schemas are implemented in the reference build.
func LoadMessageDescriptor(descDir string, ref manifest.SchemaRef) (protoreflect.MessageDescriptor, error) {
	if ref.Type != "protobuf" {
		return nil, fmt.Errorf("schema type %q: not implemented in reference build", ref.Type)
	}
	path := filepath.Join(descDir, filepath.Base(ref.Descriptor))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("descriptor %s (declared %s): %w", path, ref.Descriptor, err)
	}
	var fdset descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fdset); err != nil {
		return nil, fmt.Errorf("descriptor %s: %w", path, err)
	}
	files, err := protodesc.NewFiles(&fdset)
	if err != nil {
		return nil, fmt.Errorf("descriptor %s: %w", path, err)
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(ref.MessageType))
	if err != nil {
		return nil, fmt.Errorf("message %q not found in %s: %w", ref.MessageType, path, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q in %s is a %T, not a message", ref.MessageType, path, d)
	}
	return md, nil
}

func compareNumeric(expected, actual []byte, eps float64, out manifest.SchemaRef, descDir string) error {
	md, err := LoadMessageDescriptor(descDir, out)
	if err != nil {
		return fmt.Errorf("output %w", err)
	}

	exp := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(expected, exp); err != nil {
		return fmt.Errorf("decode expected payload as %s: %w", out.MessageType, err)
	}
	act := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(actual, act); err != nil {
		return fmt.Errorf("decode actual payload as %s: %w", out.MessageType, err)
	}
	return diffMessage("", exp, act, eps)
}

// diffMessage recursively compares two messages of the same descriptor,
// returning an error naming the first differing field path.
func diffMessage(path string, a, b protoreflect.Message, eps float64) error {
	fields := a.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		p := joinPath(path, string(fd.Name()))
		switch {
		case fd.IsMap():
			if err := diffMap(p, a.Get(fd).Map(), b.Get(fd).Map(), fd.MapValue(), eps); err != nil {
				return err
			}
		case fd.IsList():
			if err := diffList(p, a.Get(fd).List(), b.Get(fd).List(), fd, eps); err != nil {
				return err
			}
		case fd.HasPresence():
			// Messages, oneof members, and explicit optionals: set vs unset
			// is a difference, never a tolerance question.
			ha, hb := a.Has(fd), b.Has(fd)
			if ha != hb {
				return fmt.Errorf("%s: %s", p, setUnset(ha))
			}
			if !ha {
				continue
			}
			if err := diffValue(p, fd, a.Get(fd), b.Get(fd), eps); err != nil {
				return err
			}
		default:
			// Implicit-presence proto3 scalars: unset is indistinguishable
			// from the zero value, so comparing values is the whole story.
			if err := diffValue(p, fd, a.Get(fd), b.Get(fd), eps); err != nil {
				return err
			}
		}
	}
	// Fields the descriptor doesn't declare can't be tolerated numerically;
	// they must match byte-for-byte or the payloads genuinely differ.
	if !bytes.Equal(a.GetUnknown(), b.GetUnknown()) {
		where := path
		if where == "" {
			where = string(a.Descriptor().Name())
		}
		return fmt.Errorf("%s: unknown fields differ (expected %d bytes, actual %d bytes)",
			where, len(a.GetUnknown()), len(b.GetUnknown()))
	}
	return nil
}

func diffValue(path string, fd protoreflect.FieldDescriptor, a, b protoreflect.Value, eps float64) error {
	switch fd.Kind() {
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return diffFloat(path, a.Float(), b.Float(), eps)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return diffMessage(path, a.Message(), b.Message(), eps)
	default:
		// Strings, bytes, ints, bools, enums: exact.
		if !a.Equal(b) {
			return fmt.Errorf("%s: expected %v, actual %v", path, a, b)
		}
		return nil
	}
}

// diffFloat applies the numeric tolerance. NaN equals NaN and an infinity
// equals the same-signed infinity (so a deterministic model emitting them
// still passes C6); any other pairing involving NaN or Inf differs.
func diffFloat(path string, a, b, eps float64) error {
	if a == b || (math.IsNaN(a) && math.IsNaN(b)) {
		return nil
	}
	if math.Abs(a-b) <= eps { // false when either side is NaN or ±Inf
		return nil
	}
	return fmt.Errorf("%s: expected %v, actual %v (|diff| > eps %v)", path, a, b, eps)
}

func diffList(path string, a, b protoreflect.List, fd protoreflect.FieldDescriptor, eps float64) error {
	if a.Len() != b.Len() {
		return fmt.Errorf("%s: expected %d elements, actual %d", path, a.Len(), b.Len())
	}
	for i := 0; i < a.Len(); i++ {
		if err := diffValue(fmt.Sprintf("%s[%d]", path, i), fd, a.Get(i), b.Get(i), eps); err != nil {
			return err
		}
	}
	return nil
}

func diffMap(path string, a, b protoreflect.Map, valueFd protoreflect.FieldDescriptor, eps float64) error {
	// Range order is nondeterministic; sort keys so the first error reported
	// is stable across runs.
	for _, k := range sortedKeys(a) {
		if !b.Has(k) {
			return fmt.Errorf("%s[%s]: present in expected, missing in actual", path, fmtKey(k))
		}
	}
	for _, k := range sortedKeys(b) {
		if !a.Has(k) {
			return fmt.Errorf("%s[%s]: missing in expected, present in actual", path, fmtKey(k))
		}
	}
	for _, k := range sortedKeys(a) {
		p := fmt.Sprintf("%s[%s]", path, fmtKey(k))
		if err := diffValue(p, valueFd, a.Get(k), b.Get(k), eps); err != nil {
			return err
		}
	}
	return nil
}

// fmtKey renders a map key for an error path: string keys quoted, the rest as-is.
func fmtKey(k protoreflect.MapKey) string {
	if s, ok := k.Interface().(string); ok {
		return strconv.Quote(s)
	}
	return k.String()
}

func sortedKeys(m protoreflect.Map) []protoreflect.MapKey {
	keys := make([]protoreflect.MapKey, 0, m.Len())
	m.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, k)
		return true
	})
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func setUnset(expectedHas bool) string {
	if expectedHas {
		return "set in expected, unset in actual"
	}
	return "unset in expected, set in actual"
}
