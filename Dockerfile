
# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.2

# Development
FROM golang:${GO_VERSION}-alpine AS development

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

EXPOSE 8080

CMD ["go", "run", "./cmd/api"]


# Build for production
FROM development AS builder

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/barrack ./cmd/api


# Production runtime
FROM alpine:3.22 AS production

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /out/barrack /app/barrack

USER 10001:10001

EXPOSE 8080

CMD ["/app/barrack"]
