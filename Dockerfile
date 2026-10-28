# Build on the native platform and cross-compile, so multi-arch builds don't run under emulation
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0

WORKDIR /build

COPY go.mod ./
RUN go mod download

COPY . .
RUN go test ./...
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /wol . \
    && mkdir /data

# Static binary with embedded static assets, so scratch is enough
FROM scratch

COPY --from=builder /wol /wol
# Owned by nobody so a named volume mounted here is writable
COPY --from=builder --chown=65534:65534 /data /data

ENV PORT=8090 \
    DATA_FILE=/data/devices.json

# nobody
USER 65534:65534

# Run with host networking so magic packets are broadcast on the LAN
EXPOSE 8090
VOLUME /data

# scratch has no curl, so the binary checks itself
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/wol", "-healthcheck"]

ENTRYPOINT ["/wol"]
