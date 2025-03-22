FROM golang:1.22-alpine AS builder

WORKDIR /build

# Copy go mod files first for better layer caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build the Go app with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dynamic-port-mapper .

# Use a minimal Alpine image for the runtime
FROM alpine:3.19

# Install only the necessary packages for Docker client
RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    tzdata \
    wget \
    && rm -rf /var/cache/apk/*

# Copy binary from builder stage
COPY --from=builder /build/dynamic-port-mapper /app/dynamic-port-mapper
RUN chmod +x /app/dynamic-port-mapper

# Set working directory
WORKDIR /app

# Expose port 5000
EXPOSE 5000

# Run the binary
ENTRYPOINT ["/app/dynamic-port-mapper"] 
