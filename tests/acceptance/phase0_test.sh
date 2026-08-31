#!/usr/bin/env bash
#
# MSP Phase 0 acceptance gate (MSP-SPEC-001 §11). Run it with `make phase0-accept`.
#
# Six steps:
#   1. build everything: proto, descriptors (both drift-checked against the
#      committed generated code), the three images, the Go binaries, and the
#      Java bindings
#   2. the example model image passes all seven conformance checks
#   3. each of the eight negative fixtures fails the check it targets -- not
#      merely "fails", but fails THAT check, by ID, in the JSON report
#   4. predict round-trip through router-stub (MYSVC's stand-in)
#   5. predict round-trip through the real example model container
#   6. cleanup: a trap, so it fires on success, on `set -e` abort, and on Ctrl-C
#
# Requires docker, go, python3, protoc (+ grpcio-tools), and maven.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

EXAMPLE_IMAGE="msp-example-defect-cls:dev"
GOLDEN_DIR="contract/examples/defect-cls/golden"
NEGATIVE_DIR="contract/examples/negative"
MODEL_NAME="defect-cls"
STUB_PORT=19090
MODEL_PORT=18080
# Every container this script starts carries this label, so cleanup can find
# leftovers from an interrupted earlier run too -- container names alone would
# not, and a stale container holding port 18080 is the classic mystery failure.
LABEL="msp-phase0-accept=1"

STUB_PID=""
WORK="$(mktemp -d)"
START_EPOCH=$(date +%s)

# ---------------------------------------------------------------- plumbing --

remove_labelled_containers() {
	local ids
	ids="$(docker ps -aq --filter "label=$LABEL" 2>/dev/null || true)"
	if [ -n "$ids" ]; then
		# shellcheck disable=SC2086 # word splitting is the point: one id per arg
		docker rm -f $ids >/dev/null 2>&1 || true
	fi
}

cleanup() {
	# Never let cleanup itself abort: it must run to the end on every path, and
	# it must not overwrite the exit status the script is already carrying.
	set +e
	if [ -n "$STUB_PID" ]; then
		kill "$STUB_PID" >/dev/null 2>&1
		wait "$STUB_PID" >/dev/null 2>&1
	fi
	remove_labelled_containers
	rm -rf "$WORK"
}
# EXIT covers success, `set -e` abort, and explicit `exit`. INT/TERM are routed
# through `exit` so they reach the EXIT trap too (bash does not run an EXIT trap
# for an untrapped fatal signal).
trap cleanup EXIT
trap 'echo; echo "interrupted"; exit 130' INT
trap 'echo; echo "terminated"; exit 143' TERM

step() { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nPHASE 0 ACCEPTANCE FAILED: %s\n' "$*" >&2; exit 1; }

# wait_serving <target> <timeout_seconds> -- block until one golden Predict at
# <target> comes back OK. Reuses msp-traffic rather than reimplementing a health
# poll: a target that answers a golden request correctly is exactly the
# readiness the next step needs, and nothing weaker would do.
wait_serving() {
	local target="$1" timeout="$2" deadline
	deadline=$(($(date +%s) + timeout))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		if bin/msp-traffic -target "$target" -model "$MODEL_NAME" \
			-n 1 -concurrency 1 -golden-dir "$GOLDEN_DIR" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

# ------------------------------------------------------------------ step 1 --

remove_labelled_containers # leftovers from an interrupted previous run

step "step 1/6: build proto, descriptors, images, Go binaries, Java bindings"
# proto-check = proto + descriptors + "regeneration changed nothing that is
# committed". Without it this step would quietly rewrite the frozen contract's
# generated code on a host with a different protoc/grpcio-tools, and still pass.
make proto-check image-base image-example image-probe build
mvn -q -f contract/java/pom.xml verify

# ------------------------------------------------------------------ step 2 --

step "step 2/6: $EXAMPLE_IMAGE passes all seven conformance checks"
bin/msp-conform verify "$EXAMPLE_IMAGE" --json >"$WORK/example.json" ||
	fail "verify $EXAMPLE_IMAGE exited non-zero; report in $WORK/example.json"
python3 - "$WORK/example.json" <<'PY' || fail "$EXAMPLE_IMAGE did not pass all seven checks"
import json, sys

report = json.load(open(sys.argv[1]))
by_id = {c["ID"]: c for c in report["Checks"]}
failing = [f'{c["ID"]} ({c["Detail"]})' for c in report["Checks"] if not c["Pass"]]
missing = [i for i in ("C1", "C2", "C3", "C4", "C5", "C6", "C7") if i not in by_id]
for c in report["Checks"]:
    print(f'  {c["ID"]} {"PASS" if c["Pass"] else "FAIL"}  {c["Detail"].splitlines()[0]}')
if failing or missing or not report["Pass"]:
    print(f'  failing: {"; ".join(failing) or "none"} | not run: {", ".join(missing) or "none"}')
    sys.exit(1)
PY

# ------------------------------------------------------------------ step 3 --

# name|targeted check|what the fixture breaks. The name is both the Dockerfile
# suffix and the image tag suffix, so there is one string to keep in sync.
FIXTURES=(
	"c1-missing-manifest|C1|no /opt/msp/model-manifest.yaml in the image"
	"c1-bad-schema|C1|comparisonPolicy: fuzzy, rejected by manifest.schema.json"
	"c2-slow-start|C2|load() sleeps 600s against a startupSeconds: 10 budget"
	"c3-bad-status|C3|raw grpcio server: empty model_version, OK for garbage"
	"c4-wrong-type|C4|outputSchema.messageType msp.example.v1.DoesNotExist"
	"c5-wrong-golden|C5|threshold 0.9 flips golden sample-01 from defect to ok"
	"c6-nondeterministic|C6|score = random.random() differs on every call"
	"c7-oversized|C7|limits.cpu 64 exceeds the platform ceiling of 8"
)

for fixture in "${FIXTURES[@]}"; do
	IFS='|' read -r name check what <<<"$fixture"
	tag="msp-neg-$name:dev"

	step "step 3/6: negative fixture $name -> must fail $check ($what)"
	docker build -t "$tag" -f "$NEGATIVE_DIR/Dockerfile.$name" "$NEGATIVE_DIR" >"$WORK/$name.build" 2>&1 ||
		{ cat "$WORK/$name.build"; fail "fixture $name failed to build"; }

	rc=0
	bin/msp-conform verify "$tag" --json >"$WORK/$name.json" || rc=$?
	[ "$rc" -ne 0 ] || fail "fixture $name: verify exited 0, but it must not conform"

	python3 - "$check" "$WORK/$name.json" <<'PY' || fail "fixture $name did not fail its targeted check $check (report in $WORK/$name.json)"
import json, sys

check, path = sys.argv[1], sys.argv[2]
report = json.load(open(path))
by_id = {c["ID"]: c for c in report["Checks"]}
failing = [i for i, c in by_id.items() if not c["Pass"]]
missing = [i for i in ("C1", "C2", "C3", "C4", "C5", "C6", "C7") if i not in by_id]
print(f'  failing: {", ".join(failing) or "none"} | not run: {", ".join(missing) or "none"}')

targeted = by_id.get(check)
if targeted is None:
    print(f'  {check} never ran -- the fixture broke something else first')
    sys.exit(1)
if targeted["Pass"]:
    print(f'  {check} PASSED, so this fixture proves nothing: {targeted["Detail"]}')
    sys.exit(1)
print(f'  {check} FAIL as targeted: {targeted["Detail"].splitlines()[0]}')
PY
done

# ------------------------------------------------------------------ step 4 --

step "step 4/6: predict round-trip through router-stub on :$STUB_PORT"
bin/router-stub -port "$STUB_PORT" &
STUB_PID=$!
wait_serving "localhost:$STUB_PORT" 30 ||
	fail "router-stub never served a golden request on :$STUB_PORT"
bin/msp-traffic -target "localhost:$STUB_PORT" -model "$MODEL_NAME" \
	-n 50 -concurrency 4 -golden-dir "$GOLDEN_DIR" ||
	fail "traffic against router-stub exited non-zero"

# ------------------------------------------------------------------ step 5 --

step "step 5/6: predict round-trip through $EXAMPLE_IMAGE on :$MODEL_PORT"
docker run -d --label "$LABEL" -p "$MODEL_PORT:8080" "$EXAMPLE_IMAGE" >/dev/null
wait_serving "localhost:$MODEL_PORT" 60 ||
	fail "example model never served a golden request on :$MODEL_PORT"
bin/msp-traffic -target "localhost:$MODEL_PORT" -model "$MODEL_NAME" \
	-n 50 -concurrency 4 -golden-dir "$GOLDEN_DIR" ||
	fail "traffic against $EXAMPLE_IMAGE exited non-zero"

# ------------------------------------------------------------------ step 6 --

step "step 6/6: cleanup (trap)"
printf '\nPHASE 0 ACCEPTANCE PASSED in %ds\n' "$(($(date +%s) - START_EPOCH))"
