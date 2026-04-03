#!/bin/bash

# Получаем git информацию
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
VERSION=$(git describe --tags --always 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

# Собираем с встроенной версией
go build -ldflags "\
  -X main.GitCommit=${GIT_COMMIT} \
  -X main.Version=${VERSION} \
  -X main.BuildTime=${BUILD_TIME}" \
  -o claude-tg

echo "✅ Built claude-tg"
echo "   Commit: ${GIT_COMMIT}"
echo "   Version: ${VERSION}"
echo "   Time: ${BUILD_TIME}"
