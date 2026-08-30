PROTO_DIR := contract/proto

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
