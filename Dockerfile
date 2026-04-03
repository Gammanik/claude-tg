FROM golang:1.22-alpine AS builder
WORKDIR /app

# Устанавливаем git для получения версии
RUN apk add --no-cache git

# Зависимости
COPY go.mod go.sum ./
RUN go mod download

# Код и .git для версии
COPY . .

# Собираем с встроенной версией
RUN set -ex && \
    GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    VERSION=$(git describe --tags --always 2>/dev/null || echo "dev") && \
    BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S') && \
    CGO_ENABLED=0 GOOS=linux go build \
        -ldflags "-X main.GitCommit=${GIT_COMMIT} -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" \
        -o bot .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/bot .
CMD ["./bot"]
