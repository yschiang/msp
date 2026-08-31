# Negative conformance fixtures

Deliberately broken model images. Each one breaks a single thing so that exactly
one of the conformance checks C1–C7 (MSP-SPEC-001 §4.4) has to fail. They are
what turns `msp-conform verify` from a rubber stamp into a gate: without them a
check that can never fail looks identical to a check that always passes.

`tests/acceptance/phase0_test.sh` builds every fixture, runs
`msp-conform verify --json` against it, and asserts that the **targeted check ID**
is present in the report with `Pass: false`. Asserting only on the exit code
would let a fixture that fails for an unrelated reason masquerade as coverage.

| Dockerfile                        | Targeted check | What it breaks                                                        |
| --------------------------------- | -------------- | --------------------------------------------------------------------- |
| `Dockerfile.c1-missing-manifest`  | C1             | no `/opt/msp/model-manifest.yaml` in the image at all                  |
| `Dockerfile.c1-bad-schema`        | C1             | `comparisonPolicy: fuzzy`, rejected by `contract/manifest.schema.json` |
| `Dockerfile.c2-slow-start`        | C2             | `load()` sleeps 600s against a `startupSeconds: 10` budget            |
| `Dockerfile.c3-bad-status`        | C3             | raw grpcio server: empty `model_version`, `OK` for garbage payloads    |
| `Dockerfile.c4-wrong-type`        | C4             | `outputSchema.messageType: msp.example.v1.DoesNotExist`                |
| `Dockerfile.c5-wrong-golden`      | C5             | `threshold: 0.9` flips golden sample-01 from `defect` to `ok`          |
| `Dockerfile.c6-nondeterministic`  | C6             | `score = random.random()` differs on every call                        |
| `Dockerfile.c7-oversized`         | C7             | `limits.cpu: "64"` exceeds the platform ceiling of 8                   |

Build context for all of them is this directory:

```
docker build -t msp-neg-c5-wrong-golden:dev \
  -f contract/examples/negative/Dockerfile.c5-wrong-golden contract/examples/negative
```

Every fixture except `c1-missing-manifest` is `FROM msp-example-defect-cls:dev`,
so `make image-base image-example` must have run first.

## Why the manifest/config breakages are `sed` in the Dockerfile

A fixture that ships a full copy of `model-manifest.yaml` drifts: the day the
example manifest gains a field, the copy silently starts breaking two things
instead of one, and the fixture stops proving what it claims to. Editing the
inherited file in place keeps the diff to the single line named in the table.
Each edit is guarded by a `grep -q` on the exact line being replaced, so if the
example manifest ever changes shape the fixture build **fails loudly** rather
than producing an image that no longer breaks what it advertises.
