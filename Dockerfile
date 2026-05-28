# Build stage runs natively on the runner's arch (BUILDPLATFORM) and
# cross-compiles to the target arch — much faster than running the Go
# toolchain under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -tags 'netgo,osusergo' -ldflags="-s -w" -buildvcs=false \
        -o /out/art-k8s-rotate ./cmd/art-k8s-rotate

FROM scratch
# CA bundle — required for HTTPS to Artifactory and the in-cluster API server.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/art-k8s-rotate /usr/local/bin/art-k8s-rotate
# Numeric UID:GID — matches the "nonroot" convention (65532) used by
# distroless / Chainguard images, so any PodSecurity context referencing
# 65532 keeps working when this image is swapped in.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/art-k8s-rotate"]
