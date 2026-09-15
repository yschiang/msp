# AGENTS.md — workspace rules

## Rules
- **Commits:** never add a `Co-Authored-By` trailer; keep messages concise and concrete.
- **Stay in this workspace.** Do not read outside it; do not look for another team's work.

## How to talk to me
- Talk to me like I'm five years old. My brain is fried.
- Simple words. Short sentences. Only the essentials.
- If you must use a technical term, explain it right after you say it.

## Module map (spec §11 general requirement 1)

Where things live and who owns what. Full layout rules: `docs/SCAFFOLDING.md`.

| Path                              | Owns                                                                                    |
| --------------------------------- | --------------------------------------------------------------------------------------- |
| `contract/proto/`                 | The only source of truth for every message. All Go/Python/Java code is generated from it. |
| `contract/manifest.schema.json`   | What a `model-manifest.yaml` is allowed to say. Conformance check C1 validates against it. |
| `contract/sdk-python/`            | The scientist SDK (`msp` package): `BaseModel`, `serve`, the gRPC servicer, manifest loading. Also holds the committed generated Python protos. |
| `contract/base-image/`            | `msp-base:dev` — the image every model image is built FROM.                               |
| `contract/examples/defect-cls/`   | The reference model image that must pass all seven checks.                                |
| `contract/examples/negative/`     | Eight deliberately-broken images that must FAIL, one per check except C1, which gets two (missing manifest, schema-invalid manifest). They are what makes the gate a gate. |
| `contract/java/`                  | Generated grpc-java stubs for MYSVC. `src/main` is codegen only; `src/test` holds one hand-written round-trip client the acceptance gate runs against `router-stub`. |
| `msp/cmd/`                        | The four binaries: `msp-conform`, `router-stub`, `msp-traffic`, `msp-sync`.                |
| `msp/internal/conformance/`       | Checks C1–C7 and the docker orchestration behind `msp-conform verify`.                    |
| `msp/internal/{envelope,manifest,stub,traffic}/` | Envelope client + golden comparison, manifest loading, the canned ModelService, the load driver. |
| `msp/api/v1/`                     | The `ModelDeployment` CRD types (`msp.platform/v1`) and their generated deepcopy. The CRD YAML in `deploy/platform/crd/` is generated from here by `make crd`. |
| `msp/internal/syncctl/`           | The Sync Controller: state machine, digest pinning and copy, Deployment/Service/HPA builders. Unit-tested with the fake client. |
| `deploy/`                         | The GitOps tree (spec §9): CRs under `clusters/{blue,green}/deployments/`, the CRD and namespaces under `platform/`. `kubectl apply` stands in for ArgoCD. |
| `tests/acceptance/`               | `phase0_test.sh` and `phase1_test.sh`, the two gates.                                    |
| `bin/`                            | Built Go binaries (`make build`). Git-ignored.                                            |

## Frozen interfaces — changing these is a breaking change

Flag it in the commit message and get a decision ticket before touching any of them.

1. **The envelope proto** — `contract/proto/msp/serving/v1/model_service.proto`:
   `ModelService.Predict/Health`, `PredictRequest`, `PredictResponse`, `Status`.
   Every model image, MYSVC, the Router, and all four binaries speak it.
2. **The manifest schema** — `contract/manifest.schema.json`, plus its embedded
   copy `msp/internal/manifest/manifest.schema.json` (kept in sync by
   `make sync-schema`; `go test ./...` fails on drift). Fixed values live here:
   `apiVersion: msp/v1`, `contract.port: 8080`, the `comparisonPolicy` pattern.
3. **The scientist SDK surface** — `msp.BaseModel` (`load()`, `predict(bytes) -> bytes`)
   and `msp.serve(model)`. Startup order is part of the contract: the gRPC server
   starts *before* `load()`, so `Health` answers `ready=false` while loading.
   Check C2 depends on that. The SDK also serves the standard grpc.health.v1
   Health service (service name "") on the same port, NOT_SERVING until load()
   returns; Kubernetes' probes read it. Decision ticket #24.
4. **In-image paths** — `/opt/msp/model-manifest.yaml`, `/opt/msp/model.py`,
   `/opt/msp/config/`, `/opt/msp/schemas/`, `/opt/msp/golden/`.
5. **The `msp-conform verify` CLI contract** —
   `msp-conform verify <image> [--limits f.yaml] [--json] [--keep]`.
   Exit 0 only if all seven checks pass; 1 if any failed or did not run; 2 for a
   usage or infrastructure error. `--json` prints
   `{Image, Checks: [{ID, Name, Pass, Detail}], Pass}` with **stable check IDs
   `C1`…`C7`**. The negative fixtures assert on those IDs.

## Test commands

```sh
make phase0-accept                  # the Phase 0 gate: builds everything, then all of the below plus the negative fixtures
make phase1-accept                  # the Phase 1 gate: kind cluster + two registries + msp-sync; ~5 min cold
make crd-check                      # generated CRD/deepcopy drift
cd msp && go test ./...             # Go unit tests
PYTHONPATH=contract/sdk-python python3 -m pytest \
  contract/sdk-python/tests contract/examples/defect-cls/tests   # Python SDK + image smoke test
mvn -q -f contract/java/pom.xml verify                           # Java bindings compile
```

`make phase0-accept` needs docker, go, python3, protoc (with `grpcio-tools`),
and maven. It takes roughly 80s warm, a few minutes cold (the base image's
`pip install`). It cleans up its containers and background processes on every
exit path: success, `set -e` abort, and SIGINT/SIGTERM, which are routed through
the exit trap. `make phase1-accept` additionally needs kind ≥ 0.27, kubectl
and curl.
