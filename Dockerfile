# =============================================================================
# Stage 1: Build
# =============================================================================
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build arguments for version injection
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# Build the binary with optimizations and version info
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
        -X beacon/internal/version.Version=${VERSION} \
        -X beacon/internal/version.Commit=${COMMIT} \
        -X beacon/internal/version.BuildDate=${BUILD_DATE}" \
    -o /beacon ./cmd/beacon

# =============================================================================
# Stage 2: Runtime
# =============================================================================
FROM alpine:3.21

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user for security
RUN addgroup -g 1000 beacon && \
    adduser -u 1000 -G beacon -s /bin/sh -D beacon

WORKDIR /app

# Copy binary from builder
COPY --from=builder /beacon /app/beacon

# Switch to non-root user
USER beacon:beacon

# Expose default port for control plane API
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Default command
ENTRYPOINT ["/app/beacon"]
CMD ["serve"]
