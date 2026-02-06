FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    git \
    ca-certificates \
    pkg-config \
    golang-go \
    libfaketime \
    iptables \
    && rm -rf /var/lib/apt/lists/*

ENV GOPATH=/go
ENV PATH=$PATH:/go/bin

WORKDIR /infernosim

# Copy source
COPY . .

# Build Linux binary OUTSIDE the bind-mounted directory
RUN go build -o /usr/local/bin/infernosim ./cmd/agent

EXPOSE 18080 19000

ENTRYPOINT ["/usr/local/bin/infernosim"]
