PROTO_DIR := contract/proto
SCHEMA_SRC := contract/manifest.schema.json
SCHEMA_DST := msp/internal/manifest/manifest.schema.json
# Everything `proto` and `descriptors` write and git tracks. proto-check asserts
# regeneration changed none of it.
GEN_PATHS := msp/gen contract/sdk-python/msp/serving contract/sdk-python/msp/example \
  contract/examples/defect-cls/schemas

.PHONY: proto
proto:
	mkdir -p msp/gen contract/sdk-python
	protoc -I $(PROTO_DIR) \
	  --go_out=msp/gen --go_opt=module=github.com/yschiang/msp/msp/gen \
	  --go-grpc_out=msp/gen --go-grpc_opt=module=github.com/yschiang/msp/msp/gen \
	  $(PROTO_DIR)/msp/serving/v1/model_service.proto $(PROTO_DIR)/msp/example/v1/defect.proto
	python3 -m grpc_tools.protoc -I $(PROTO_DIR) \
	  --python_out=contract/sdk-python --pyi_out=contract/sdk-python \
	  --grpc_python_out=contract/sdk-python \
	  $(PROTO_DIR)/msp/serving/v1/model_service.proto $(PROTO_DIR)/msp/example/v1/defect.proto

.PHONY: descriptors
descriptors:
	mkdir -p contract/examples/defect-cls/schemas
	protoc -I $(PROTO_DIR) --include_imports \
	  --descriptor_set_out=contract/examples/defect-cls/schemas/input.desc \
	  $(PROTO_DIR)/msp/example/v1/defect.proto
	cp contract/examples/defect-cls/schemas/input.desc contract/examples/defect-cls/schemas/output.desc

# Drift guard for the committed generated code, the protos' equivalent of
# sync-schema: regenerate, then assert nothing moved. A host with a different
# protoc-gen-go or grpcio-tools would otherwise rewrite the frozen contract --
# including the Python import-time version floors -- inside a green build.
# `git status --porcelain`, not `git diff`: it also catches files a newer
# generator adds.
.PHONY: proto-check
proto-check: proto descriptors
	@drift="$$(git status --porcelain -- $(GEN_PATHS))"; \
	if [ -n "$$drift" ]; then \
	  echo "generated-code drift: regenerating changed committed artifacts:"; \
	  echo "$$drift"; \
	  echo "Your protoc / protoc-gen-go / grpcio-tools emits something other than what is committed."; \
	  echo "Either commit the regenerated files (git add -- $(GEN_PATHS)) if the new toolchain is the"; \
	  echo "intended one, or install the toolchain versions in docs/SCAFFOLDING.md and re-run."; \
	  exit 1; \
	fi

.PHONY: image-base
image-base:
	docker build -t msp-base:dev -f contract/base-image/Dockerfile contract/

# Build context is contract/examples/defect-cls/ itself -- the Dockerfile's COPY
# sources (model.py, config/, schemas/, golden/) are bare paths relative to it.
# image-base is a prerequisite because this is the one ordering `make -j images`
# would otherwise get wrong (this Dockerfile is FROM msp-base:dev). The other
# inputs -- generated Python in the SDK, schemas/*.desc -- are committed, so no
# codegen target is a prerequisite of any image target.
.PHONY: image-example
image-example: image-base
	docker build -t msp-example-defect-cls:dev \
	  -f contract/examples/defect-cls/Dockerfile contract/examples/defect-cls

# Static cross-compile (linux, host arch) so the binary runs in FROM scratch.
# The binary lands in msp/ (git-ignored) because the docker build context is msp/.
.PHONY: image-probe
image-probe:
	cd msp && CGO_ENABLED=0 GOOS=linux go build -o msp-conform ./cmd/msp-conform
	docker build -t msp-conform-probe:dev -f msp/Dockerfile.probe msp
	rm msp/msp-conform

# All three Go binaries into bin/ (git-ignored), named exactly as the spec's
# CLIs. Separate from image-probe's output on purpose: that one cross-compiles a
# static linux binary to msp/msp-conform because the probe image's build context
# is msp/, and deletes it again. Same source, different GOOS, different path --
# so the two targets never overwrite each other's output.
.PHONY: build
build:
	mkdir -p bin
	cd msp && go build -o ../bin/ ./cmd/...

.PHONY: images
images: image-base image-example image-probe

# Everything that runs without docker: the Go tests, the Python SDK tests, and
# the schema drift check. The example-image smoke test lives under
# contract/examples and skips itself when docker is missing.
.PHONY: test
test: sync-schema
	cd msp && go test ./...
	PYTHONPATH=contract/sdk-python python3 -m pytest \
	  contract/sdk-python/tests contract/examples/defect-cls/tests

# The Phase 0 acceptance gate (MSP-SPEC-001 §11). Builds everything itself.
.PHONY: phase0-accept
phase0-accept:
	bash tests/acceptance/phase0_test.sh

# Copy the canonical manifest schema over its //go:embed-ed copy. Doubles as the
# drift check: it exits non-zero when the copy was stale, so CI can just run it.
# (`go test ./...` catches the same drift via TestSchemaCopyMatchesCanonical.)
.PHONY: sync-schema
sync-schema:
	cp $(SCHEMA_SRC) $(SCHEMA_DST)
	@git diff --quiet -- $(SCHEMA_DST) || { \
	  echo "schema drift: $(SCHEMA_DST) was stale and has been re-synced; commit it"; exit 1; }
