# Multi-Stage Distroless Dockerfile for rag-kb-catalog-service (Go 1.26+)
# Hardened against CVEs and enforces non-root execution (UID 65532).

# Stage 1: Build static Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Install CA certificates and tzdata for minimal runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and documentation/OpenAPI assets
COPY . .

# Compile static binary with optimizations and trimmed paths
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w -extldflags '-static'" \
    -o /bin/catalog-service ./cmd/catalog-service

# Stage 2: Distroless Non-Root Runtime Container
FROM gcr.io/distroless/static:nonroot

WORKDIR /

# Copy CA certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy compiled static microservice binary
COPY --from=builder /bin/catalog-service /catalog-service

# Enforce non-root execution (UID/GID 65532: nonroot)
USER 65532:65532

# Expose ports: HTTP REST (8080), gRPC HTTP/2 (50051), Dedicated Metrics (9090)
EXPOSE 8080 50051 9090

# Default entrypoint
ENTRYPOINT ["/catalog-service"]

