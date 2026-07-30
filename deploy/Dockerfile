# syntax=docker/dockerfile:1

# --- build -----------------------------------------------------------------
FROM golang:1.26-alpine AS build

# git is needed for module fetches over VCS; ca-certificates for HTTPS.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Copy manifests first so the dependency layer is cached independently of source
# changes — most commits do not touch go.mod.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
# CGO off produces a static binary that runs on a scratch-like base.
# -s -w strips the symbol table and DWARF data, cutting image size.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X github.com/sabin-bhattarai/ims-backend/internal/api.Version=${VERSION}" \
      -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/seed ./cmd/seed

# --- runtime ---------------------------------------------------------------
FROM alpine:3.20 AS runtime

# wget serves the container healthcheck; tzdata so timestamps localise correctly.
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S -g 10001 ims \
 && adduser -S -u 10001 -G ims ims

WORKDIR /app

COPY --from=build /out/api /app/api
COPY --from=build /out/worker /app/worker
COPY --from=build /out/seed /app/seed

# Never run as root: a container escape should not land on uid 0.
USER 10001:10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health/ready || exit 1

ENTRYPOINT ["/app/api"]
