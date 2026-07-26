# Build stage
ARG LDFLAGS
FROM golang:1.25.12-alpine3.22 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="${LDFLAGS}" -o infernosim ./cmd/agent

# Final stage
FROM alpine:3.22

RUN apk --no-cache add ca-certificates iptables \
    && addgroup -S -g 65532 infernosim \
    && adduser -S -D -H -u 65532 -G infernosim infernosim \
    && mkdir -p /app /incident \
    && chown -R infernosim:infernosim /app /incident

WORKDIR /app

# Copy the pre-built binary from builder stage
COPY --from=builder /app/infernosim /usr/local/bin/infernosim

USER infernosim

EXPOSE 18080 19000

ENTRYPOINT ["/usr/local/bin/infernosim"]
