# Stage 1: Build Babylon binary
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and compile statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /babylon ./cmd/babylon

# Stage 2: Runtime image with Python and Pulumi
FROM python:3.12-slim-bookworm

# Install essential runtime tools: git, ca-certificates, curl
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Pulumi CLI
RUN curl -fsSL https://get.pulumi.com | sh
ENV PATH="/root/.pulumi/bin:${PATH}"

WORKDIR /app

# Copy compiled Babylon binary from builder stage
COPY --from=builder /babylon /app/babylon

# Default directory for local PR workspaces
RUN mkdir -p /tmp/babylon/workspaces

# Babylon webhook server port
EXPOSE 8090

ENTRYPOINT ["/app/babylon"]
