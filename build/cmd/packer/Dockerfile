# Build from the repository root, as the module replaces its siblings:
#   docker build -f build/cmd/packer/Dockerfile .
# Go cross-compiles on the build platform, so arm64 needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
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
