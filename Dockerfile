# Cubit ships as a static binary on scratch: no shell, no package manager, no
# libc. The only things in the final image are CA certificates, an empty /data
# owned by the runtime user, and the binary itself.

# Stage 1: build. Pinned to the build platform so Go cross-compiles natively
# rather than the whole toolchain running under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /app
ENV GOTOOLCHAIN=local
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} go build \
    -trimpath \
    -ldflags="-s -w -X github.com/t0mer/cubit/internal/version.Version=${VERSION}" \
    -o /app/cubit ./cmd/cubit

# scratch has no shell to mkdir or chown at runtime, so the data directory is
# prepared here with the ownership the non-root user needs to write the
# encrypted session file.
RUN mkdir -p /prepared/data && chown -R 10001:10001 /prepared/data

# Stage 2: runtime.
FROM scratch

# The client speaks TLS to a public API, and scratch has no trust store.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder --chown=10001:10001 /prepared/data /data
COPY --from=builder /app/cubit /cubit

USER 10001:10001

EXPOSE 8080
VOLUME ["/data"]

# scratch has no shell and no curl, so liveness is the binary answering for
# itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/cubit", "--version"]

ENTRYPOINT ["/cubit"]
