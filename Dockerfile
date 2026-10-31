# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

# Download modules in their own layer so code changes reuse the cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/devices-api ./cmd/api && \
    go build -trimpath -ldflags="-s -w" -o /out/tokengen ./cmd/tokengen

# Development helper that mints local access tokens. Built only on request
# (docker compose run --rm tokengen); it is not part of the API image.
FROM gcr.io/distroless/static-debian12:nonroot AS tokengen
COPY --from=build /out/tokengen /usr/local/bin/tokengen
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tokengen"]

# API image. Distroless static: no shell or package manager, runs as an
# unprivileged user. Kept last so it is the default build target.
FROM gcr.io/distroless/static-debian12:nonroot AS api
COPY --from=build /out/devices-api /usr/local/bin/devices-api
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/devices-api", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/devices-api"]
