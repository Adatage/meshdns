FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dns-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dnsctl     ./cmd/cli


FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /dns-server /usr/local/bin/dns-server
COPY --from=builder /dnsctl     /usr/local/bin/dnsctl

EXPOSE 53/udp 53/tcp 50051/tcp

ENTRYPOINT ["/usr/local/bin/dns-server"]