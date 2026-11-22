# syntax=docker/dockerfile:1
#
# quietbox container image: one static binary on distroless, running as nonroot.
# No config, password or state is baked in: mount /config (read-only), /data (the
# SQLite state) and the password as a secret. See docs/DEPLOY.md.
#
# The build stage runs on the build machine's own platform and cross-compiles
# (pure Go, CGO off), so a multi-arch build needs no QEMU:
#   docker buildx build --platform linux/amd64,linux/arm64 -t quietbox .

# golang:1.26 (1.26.11), multi-arch index digest
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/quietbox ./cmd/quietbox

# gcr.io/distroless/static-debian12:nonroot, multi-arch index digest.
# Has CA certificates and tzdata, no shell.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# Version and revision labels are added at build time (`make docker-build`, CI):
# --label org.opencontainers.image.version=... (a build-arg here would be cached
# stale by podman/buildah).
LABEL org.opencontainers.image.title="quietbox" \
      org.opencontainers.image.description="Self-hosted IMAP triage daemon: moves non-urgent mail out of the inbox, learning from how you file it" \
      org.opencontainers.image.source="https://github.com/michele/quietbox"
COPY --from=build /out/quietbox /quietbox
USER 65532:65532
ENTRYPOINT ["/quietbox"]
CMD ["run", "--config", "/config/quietbox.toml"]
