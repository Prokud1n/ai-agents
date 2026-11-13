# syntax=docker/dockerfile:1

# golang:1.26-alpine
FROM golang:1.26-alpine@sha256:30b1e546a9c7fa3b747c2bdba3a0c7bdf3a02ebb4c5b8405d2f948d7e8aac4ec AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app . && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-run ./cmd/agent-run

# Tools that need ffmpeg (transcribe) are built separately, so the app image below stays unchanged.
FROM build AS build-media

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out-media/transcribe ./cmd/transcribe

# Media image: ffmpeg + transcribe. Build with `docker build --target media .`
# alpine:3.24
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS media

# ffmpeg is pinned to the 8.1 branch of alpine 3.24 (patch releases and security fixes still apply).
RUN apk add --no-cache "ffmpeg=~8.1" && \
    addgroup -S -g 65532 nonroot && \
    adduser -S -D -H -u 65532 -G nonroot -s /sbin/nologin nonroot && \
    install -d -o 65532 -g 65532 /work

COPY --from=build-media /out-media/ /app/

USER 65532:65532
# Mount the host directory with videos here; -out is written relative to it.
WORKDIR /work

ENTRYPOINT ["/app/transcribe"]

# The app image is the last stage: it is the default target of `docker build` and docker compose.
# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/ /app/
# Agent definitions (AGENTS_DIR=./agents is relative to the working directory).
COPY agents/ /app/agents/
WORKDIR /app

USER nonroot:nonroot
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=5 \
    CMD ["/app/healthcheck"]

ENTRYPOINT ["/app/app"]
