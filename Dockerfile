# Pure-Go SQLite means CGO_ENABLED=0 links a static binary, so the final image
# needs nothing around it — no libc, no shell, no package manager.
#
# The builder is pinned to the *build* platform and cross-compiles to the target
# instead of being emulated: Go needs no toolchain per architecture, so the
# arm64 image is compiled natively on an amd64 runner rather than through QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags "-X main.version=$VERSION" -o /overlite ./cmd/overlite

FROM scratch
COPY --from=build /overlite /overlite
# The database lives here, so `-v ./sample.db:/data/sample.db` lands next to the
# working directory and a bare file name on the command line resolves to it.
WORKDIR /data
EXPOSE 5432
# 127.0.0.1 inside a container is reachable only from the container itself, so
# the image has to bind the wildcard address to be of any use. Arguments given
# to `docker run` are appended, which leaves `overlite sample.db` reading as it
# does outside a container.
ENTRYPOINT ["/overlite", "--host", "0.0.0.0"]
