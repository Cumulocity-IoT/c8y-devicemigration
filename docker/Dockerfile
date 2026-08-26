# The microservice image. Two stages so the deployed image holds a static binary
# and nothing else: no shell, no package manager, no Go toolchain.

# BUILDPLATFORM, not the target: the compiler runs natively and cross-compiles
# through GOARCH below. Pinning this stage to linux/amd64 instead would put the
# whole Go build under emulation on an Apple Silicon machine, which is minutes
# rather than seconds for the same output.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

# Set by buildx from --platform. The default covers a plain `docker build`, and
# amd64 is the only architecture the platform runs.
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# What the running service calls itself. Passed in by `make service-package`,
# because the build happens inside this container and cannot see the tag: without
# it a deployed microservice reports "dev", which it did.
ARG VERSION=dev

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# CGO off: the target image has no libc to link against.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags="-s -w -X github.com/Cumulocity-IoT/c8y-devicemigration/internal/version.Version=$VERSION" \
    -o /out/c8y-devicemigration-service ./cmd/c8y-devicemigration-service

# A migration stages one device's data at a time under $TMPDIR, so the image
# needs a writable /tmp - which scratch has no way to create. An empty directory
# holds no architecture, so building it here rather than in a second runtime
# image costs nothing.
RUN mkdir -p /staging

FROM scratch

# Root certificates, so the client can verify the platform's certificate. The
# only thing this image takes from a distribution.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /staging /tmp
COPY --from=build /out/c8y-devicemigration-service /c8y-devicemigration-service

# Unprivileged: nothing here needs root, and the platform does not require it.
USER 65532:65532

# The platform requires an inbound REST API on port 80 and probes /health there.
EXPOSE 80
ENV TMPDIR=/tmp

ENTRYPOINT ["/c8y-devicemigration-service"]
