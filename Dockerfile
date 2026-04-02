FROM golang:1.22-alpine AS builder
WORKDIR /app

# Зависимости
COPY go.mod go.sum ./
RUN go mod download

# Код
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o bot .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/bot .
CMD ["./bot"]
