# SCAFFOLDING.md — Monorepo Layout & Build Conventions

Authoritative spec for where code lives, how each language island builds, and how
code is generated from the contract. Every phase ticket follows this document;
changes to it are breaking and need a decision ticket on wayfinder map #1.

## Decision: implementation languages

- **Platform (Router, controllers, workers, conformance tooling): Go**
  (MSP-SPEC-001 §11 tech stack; controller-runtime in Phase 1 requires Go, and Phase 0
  tooling shares envelope code with the future Router).
- **MYSVC: Java 17 + SpringBoot** (MYSVC-SPEC-001). The contract ships a Java
  bindings module (`contract/java/`) generated from the same proto so MYSVC
  consumes ready-made grpc-java stubs.
- **Scientist SDK / base image / example models: Python 3.12** (mandated by spec §4.5).

## Layout

```
contract/                      # the frozen contract — everything a model image must obey
  proto/                       # single source of truth for all codegen
    msp/serving/v1/model_service.proto     # envelope (spec §4.1)
    msp/example/v1/defect.proto            # example model payload schema
  manifest.schema.json         # JSON Schema for model-manifest.yaml (C1)
  sdk-python/                  # scientist SDK package `msp` + tests
    msp/serving/v1/, msp/example/v1/   # GENERATED Python (committed; natural proto package paths)
  java/                        # Java bindings module for MYSVC (Maven, codegen only)
    pom.xml
  base-image/Dockerfile        # msp-base image
  examples/                    # example + negative-case model images
    defect-cls/
    negative/
msp/                           # platform tools & services (Go module github.com/yschiang/msp/msp)
  go.mod
  gen/                         # GENERATED Go proto (committed)
  api/v1/                      # ModelDeployment CRD types + controller-gen markers; zz_generated.deepcopy.go committed
  cmd/                         # one dir per binary: msp-conform, router-stub, msp-traffic, msp-sync
  internal/                    # envelope, manifest, conformance, stub, traffic, syncctl
  Dockerfile.probe             # conformance probe image (FROM scratch + static binary)
mysvc/                         # MYSVC service (Java/SpringBoot; its own tickets)
deploy/                        # GitOps tree (spec §9); `kubectl apply` stands in for ArgoCD locally
  clusters/{blue,green}/deployments/   # ModelDeployment CRs
  routes/                      # ModelRoute CRs (Phase 2)
  platform/crd/                # GENERATED CRD YAML (make crd), committed
  platform/namespaces.yaml     # msp-blue / msp-green
  policies/
docs/                          # specs, plans, ADR-like decisions
bin/                           # built Go binaries (`make build`, git-ignored)
Makefile                       # top-level entry points; delegates to go/mvn/pip/docker
```

## Build rules

- **Go**: single module `msp/`, requires **Go 1.23+**; `cd msp && go build ./... && go test ./...`
  must pass from a clean checkout with only Go + Docker installed. Binaries land in
  `bin/` via `make build`, named exactly as the spec's CLIs (`msp-conform`,
  `router-stub`, `msp-traffic`, `msp-sync`).
- **Java bindings**: Maven only (no Gradle). `mvn -q -f contract/java/pom.xml verify`
  compiles generated stubs with JDK 17. No hand-written Java in `src/main` — the
  jar is pure codegen. `src/test` holds exactly one class,
  `io.msp.contract.RouterStubRoundTrip`, run by the acceptance gate via
  `exec:java` once router-stub is listening: it is the §11 item "MYSVC 端以
  Router stub 完成一次 predict 往返", and nothing else proves these stubs dial.
- **Python**: `pip install -e contract/sdk-python[dev]`; tests with `pytest`.
- **Images**: built via Makefile targets, tagged `<name>:dev` locally
  (`msp-base:dev`, `msp-example-defect-cls:dev`, `msp-conform-probe:dev`).
  The one exception is `contract/examples/negative/`: its eight `msp-neg-*:dev`
  fixtures are built by `tests/acceptance/phase0_test.sh` itself, because the
  same loop already carries each fixture's name, targeted check ID and
  description. A Makefile target would be a second copy of that list to keep in
  sync, which is the drift `contract/examples/negative/README.md` argues against.
  Nothing outside the acceptance gate builds or consumes them.
- **Kubernetes**: the Sync Controller uses the controller-runtime library (no kubebuilder
  CLI, no ArgoCD locally). `make crd` runs `controller-gen` via `go run` (pinned version,
  nothing on PATH) to write `msp/api/v1/zz_generated.deepcopy.go` and
  `deploy/platform/crd/`; both are committed and `make crd-check` fails on drift, like
  `proto-check`. `make phase1-accept` needs kind ≥ 0.27, kubectl and curl in addition to
  Phase 0's tools; it uses a kind cluster named `msp` (reused if present) and two
  `registry:2` containers on 127.0.0.1:5010 / :5011.
- **Top-level Makefile** is the developer entry point: `make proto descriptors
  proto-check build images test phase0-accept`. CI (when it exists) calls the same
  targets. `test` runs the Go tests and the Python tests — the schema drift check
  is one of the Go tests (`TestSchemaCopyMatchesCanonical`), so `test` writes
  nothing and needs no git; run `make sync-schema` when it tells you to;
  `images` builds all three images; `proto-check` regenerates and then asserts the
  committed generated code did not move.

## Codegen rules

- All protobuf code is generated from `contract/proto/` only. No hand-written
  message types.
- **Go**: generated by `make proto` into `msp/gen/` — **committed**, so builds
  don't require protoc. `make proto-check` regenerates and fails if any committed
  generated file changed; the acceptance gate runs it instead of bare `proto`.
- **Java**: generated at build time by `protobuf-maven-plugin` into
  `contract/java/target/` — never committed.
- **Python**: generated by `python3 -m grpc_tools.protoc` via `make proto` with
  `--python_out=contract/sdk-python` — files land on their natural proto package
  paths (`msp/serving/v1/…`) inside the SDK package and are **committed**, so
  images and tests build without protoc and imports need no rewriting.
- **Python runtime floor**: the committed gencode embeds a hard, import-time version
  check — `protobuf>=7.35.1` (`ValidateProtobufRuntimeVersion` in `*_pb2.py`) and
  `grpcio>=1.83.1` (`GRPC_GENERATED_VERSION` in `*_pb2_grpc.py`). Below either floor,
  `import` raises, not warns. Task 3's `contract/sdk-python/pyproject.toml` and the
  `python:3.12-slim` base image must pin `protobuf` and `grpcio` at or above these
  versions, or every model image fails at model import.
- **Descriptor sets** (`*.desc`, used by manifests) are generated by
  `make descriptors` and committed next to the example model.
- **CRD**: generated from the Go types in msp/api/v1 by make crd; committed;
  crd-check guards drift.
- Proto and `manifest.schema.json` changes are **breaking**: flag in the PR/commit
  message and require a decision ticket if the contract is already frozen (S1).

## Naming

- Go packages: `msp/internal/<area>` (`envelope`, `manifest`, `conformance`, `stub`, `traffic`, `syncctl`).
- Proto packages: `msp.<area>.v<N>`; go_package `github.com/yschiang/msp/msp/gen/<area>v<N>`;
  java_package `io.msp.gen.<area>.v<N>`.
- Kubernetes: CRD group msp.platform, version v1; child objects carry labels
  msp.platform/deployment, msp.platform/model, msp.platform/version.
- Repo artifacts (code, commits, docs, issues) in English.
