# msp-example-defect-cls: Phase 0 example model image (MSP-SPEC-001 §4.5).
# Build context is contract/examples/defect-cls/.
FROM msp-base:dev
COPY model.py /opt/msp/model.py
COPY model-manifest.yaml /opt/msp/model-manifest.yaml
COPY config/ /opt/msp/config/
COPY schemas/ /opt/msp/schemas/
COPY golden/ /opt/msp/golden/
CMD ["python", "/opt/msp/model.py"]
