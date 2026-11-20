# Build from the repository root, as the module replaces its siblings:
#   docker build -f build/cmd/packer/Dockerfile .
# Go cross-compiles on the build platform, so arm64 needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
ARG TARGETOS TARGETARCH
WORKDIR /src/build
RUN --mount=target=/src,rw \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags='-s -w' -o /pack ./cmd/packer

FROM scratch
COPY --from=build /pack /pack
# push needs a CA bundle for HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/pack"]
