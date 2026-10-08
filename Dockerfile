# Cross-compiling from the build platform, so a multi-arch build needs no
# emulated toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETARCH
ARG version=dev
WORKDIR /src
# go.sum only exists once there are dependencies.
COPY go.mod go.su[m] ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/containerd-registry ./cmd/containerd-registry

# The binary is static, so the image needs nothing else: no shell or tools for
# anyone who gets code execution in a container holding the containerd socket.
FROM scratch
# The containerd socket is owned by root.
USER 0:0
COPY --from=build /out/containerd-registry /containerd-registry
ENTRYPOINT ["/containerd-registry"]
