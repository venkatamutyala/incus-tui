# syntax=docker/dockerfile:1

# ---- build stage ----
# Build on the native runner platform and cross-compile to the target arch (the binary
# is pure Go / CGO-free), so the arm64 image doesn't compile the whole tree under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/incus-tui ./cmd/incus-tui

# ---- runtime stage ----
# debian-slim + the Incus client so the in-TUI shell-in (`incus exec`) works.
# (For a tiny image without shell-in, swap this stage for gcr.io/distroless/static.)
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
    && install -d /etc/apt/keyrings \
    && curl -fsSL --proto '=https' https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc \
    # Pin the Zabbly signing key by fingerprint so a swapped/forged key fails the build.
    && gpg --show-keys --with-colons /etc/apt/keyrings/zabbly.asc \
        | awk -F: '$1=="fpr"{print $10}' \
        | grep -qx 4EFC590696CB15B87C73A3AD82CC8797C838DCFD \
    && . /etc/os-release \
    && printf 'Enabled: yes\nTypes: deb\nURIs: https://pkgs.zabbly.com/incus/stable\nSuites: %s\nComponents: main\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/zabbly.asc\n' \
        "$VERSION_CODENAME" "$(dpkg --print-architecture)" > /etc/apt/sources.list.d/zabbly-incus-stable.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends incus-client \
    && apt-get purge -y curl gnupg \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/incus-tui /usr/local/bin/incus-tui
ENV INCUS_SOCKET=/var/lib/incus/unix.socket
# SECURITY: this image talks to Incus over the host's admin unix socket. Bind-mounting
# that socket grants the container host-root-equivalent control of Incus, so only run it
# on a trusted single-user host. For most users the static binary on the host is simpler
# and safer. The container runs as root by default so it can reach the (root-owned)
# socket; pass --user to override when your socket permissions allow it.
ENTRYPOINT ["incus-tui"]
