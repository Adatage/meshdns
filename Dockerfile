FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache protoc protobuf-dev

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN mkdir -p pkg/proto && \
    protoc \
      -I api/proto \
      --go_out=pkg/proto      --go_opt=paths=source_relative \
      --go-grpc_out=pkg/proto --go-grpc_opt=paths=source_relative \
      api/proto/dns.proto

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dns-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dnsctl     ./cmd/cli


FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /dns-server /usr/local/bin/dns-server
COPY --from=builder /dnsctl     /usr/local/bin/dnsctl

EXPOSE 53/udp 53/tcp 50051/tcp

ENTRYPOINT ["/usr/local/bin/dns-server"]