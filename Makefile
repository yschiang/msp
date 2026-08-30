PROTO_DIR := contract/proto
SCHEMA_SRC := contract/manifest.schema.json
SCHEMA_DST := msp/internal/manifest/manifest.schema.json

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

# Copy the canonical manifest schema over its //go:embed-ed copy. Doubles as the
# drift check: it exits non-zero when the copy was stale, so CI can just run it.
# (`go test ./...` catches the same drift via TestSchemaCopyMatchesCanonical.)
.PHONY: sync-schema
sync-schema:
	cp $(SCHEMA_SRC) $(SCHEMA_DST)
	@git diff --quiet -- $(SCHEMA_DST) || { \
	  echo "schema drift: $(SCHEMA_DST) was stale and has been re-synced; commit it"; exit 1; }
