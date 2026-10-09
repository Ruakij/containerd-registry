# containerd-registry

[![Helm](https://img.shields.io/badge/dynamic/yaml?url=https%3A%2F%2Fruakij.github.io%2Fcontainerd-registry%2Findex.yaml&query=%24.entries%5B%27containerd-registry%27%5D%5B0%5D.version&label=Helm&logo=helm&color=0F1689&prefix=v)](#install)
[![containerd](https://img.shields.io/badge/containerd-CRI-0A7BBB?logo=containerd&logoColor=white)](#requirements)

**A pull-through registry whose storage is the containerd of its node.**

A DaemonSet that serves the OCI distribution API (Docker Registry HTTP API V2)
from the content store and images of the node containerd. An image missing
there is pulled by containerd itself, the way kubelet pulls it, so the only
copy is the one the node keeps anyway. Push, authentication and access control
are planned.

## Why

A registry mirror such as `registry:2` or zot stores every image it serves in
its own storage, so a node running it holds each image twice, and it pulls
past the registry config of the node, Spegel included. Mounting the containerd
socket into CI jobs instead gives them root on the node. Here only the
registry holds the socket, and clients such as dind get registry operations.

## Features

- Pull-through for the upstreams listed in the config; any other answers 404,
  so clients cannot make the node pull from arbitrary registries.
- Pulls through the CRI image service: mirrors, rewrites, credentials and
  Spegel from the registry config of the node apply, and the images are
  labelled and garbage collected like any image kubelet pulled.
- Concurrent requests for the same image share one pull.
- Serves Docker `--registry-mirror` requests, containerd mirror requests
  (`?ns=<host>`) and plain clients naming the upstream as first path segment
  (`<registry>/ghcr.io/org/app`).
- Blobs support range requests.

## Requirements

- containerd with the CRI plugin, the runtime of kubelet. Tested with
  containerd 2.3 on k3s.
- Pulls fetch the platform of the node. Manifests of other platforms answer
  only once requested by digest, which pulls them; attestation manifests
  answer 404.

## Install

From the Helm repository:

```sh
helm repo add containerd-registry https://ruakij.github.io/containerd-registry
helm repo update
helm install containerd-registry containerd-registry/containerd-registry -n kube-system
```

Or straight from the OCI registry:

```sh
helm install containerd-registry oci://ghcr.io/ruakij/charts/containerd-registry -n kube-system
```

On k3s and RKE2, add `--set containerdSocket=/run/k3s/containerd/containerd.sock`.
The Service has `internalTrafficPolicy: Local`, so a client talks to the
containerd of its own node. For dind:

```sh
dockerd --registry-mirror=http://containerd-registry.kube-system:5000 \
  --insecure-registry=containerd-registry.kube-system:5000
```

## Configuration

`config.yml`, JSON or YAML, under `config` in the chart values. `http` follows
the [zot](https://zotregistry.dev/) format, `proxy` the containerd and k3s
registry config:

```yaml
log:
  level: info              # debug, info, warn, error
http:
  address: 0.0.0.0
  port: "5000"
  tls: {cert: ..., key: ...}   # optional
proxy:
  default: docker.io       # upstream of requests that name none
  notFoundTTL: 10s         # how long a ref upstream does not have answers 404 without asking again
  registries:              # allowed upstreams
    docker.io: {}
    ghcr.io: {}
storage:
  containerd:
    address: /run/containerd/containerd.sock
```

Unknown keys are rejected. Endpoints, mirrors, TLS and credentials of the
upstreams belong in the registry config of the node.

## How it works

1. The upstream of a request is its `?ns=` parameter, else a first path segment
   with a dot, a port or `localhost`, else `proxy.default`. Single-segment
   Docker Hub names get `library/`, as Docker does.
2. A tag is looked up as image `<upstream>/<name>:<tag>` in the `k8s.io`
   namespace, a digest in the content store. Its bytes are served unchanged and
   checked against the digest.
3. A miss is pulled with the CRI `PullImage`, and looked up again. A ref
   upstream lacked within `proxy.notFoundTTL`, and the attestation manifests
   listed in served indexes, answer 404 without a pull.
4. Blobs are read from the content store; they arrived with their manifest.

## Development

```sh
git config core.hooksPath .githooks  # gofmt, vet, tests and lint before each commit
make build                           # bin/containerd-registry
CONTAINERD_ADDRESS=/run/containerd/containerd.sock go test -tags containerd ./internal/store  # pulls busybox, on a node
```
