# msp-base: the image every model image is built FROM (MSP-SPEC-001 4.5).
# Build context is contract/.
FROM python:3.12-slim
COPY sdk-python /opt/msp-sdk
RUN pip install --no-cache-dir /opt/msp-sdk
# Contract: model code + weights + config live in the child image under /opt/msp
EXPOSE 8080
