# Build stage
ARG LDFLAGS
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o infernosim ./cmd/agent

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates libfaketime iptables

WORKDIR /app

# Copy the pre-built binary from builder stage
COPY --from=builder /app/infernosim /usr/local/bin/infernosim

EXPOSE 18080 19000

ENTRYPOINT ["/usr/local/bin/infernosim"]
