# containerd-registry

**An OCI registry whose storage is the containerd of its node.**

A DaemonSet that serves the OCI distribution API (Docker Registry HTTP API V2)
from the content store and image records of the node containerd. An image
missing there is pulled by containerd itself, through the registry config of
the node (on k3s: Spegel peers first, then upstream), so the only copy is the
one the kubelet uses anyway. Only the registry holds the containerd socket;
clients such as CI job pods get registry operations, not root on the node.

Push, delete, htpasswd and token auth and access rules are planned.

## Status

Scaffold only: the binary parses its flags and exits with an error, it serves
nothing yet.

## Configuration

`config.yml` (JSON or YAML). `http` follows the [zot format](https://zotregistry.dev/),
`proxy` the containerd/k3s registry model:

- `distSpecVersion`, `log`.
- `http`: `address`, `port`, `tls`, `realm`, `auth.htpasswd`, `auth.bearer`,
  `auth.failDelay`, `accessControl` (zot semantics: the longest matching
  repository glob decides; actions `read`, `create`, `update`, `delete`).
- `proxy`: `default` (upstream of requests that name none, e.g. Docker
  `--registry-mirror` requests) and `registries.<host>` (allowed upstreams,
  keyed by host as in containerd `certs.d/<host>`, with an optional k3s-style
  `rewrite`). Endpoints, mirrors, TLS and upstream credentials come from the
  registry config of the node, which containerd pulls with.
- `storage.containerd`: `address` (containerd socket), `pullNamespace`
  (pulled-through images, default `k8s.io`), `pushNamespace` (pushed images),
  `hostsDir` (registry config of the node). `storage.retention` for pushed
  images.

Any other key is rejected.

Clients name the upstream with containerd's `?ns=<host>` parameter, with a
host as first path segment (`<registry>/ghcr.io/org/app`), or not at all for
`proxy.default`.

## Install

The Helm chart in `charts/containerd-registry` runs a DaemonSet with the
containerd socket of the node mounted, and a Service with
`internalTrafficPolicy: Local`, so a client talks to the containerd of its own
node. On k3s and RKE2 the socket is `/run/k3s/containerd/containerd.sock`:

```sh
helm install containerd-registry charts/containerd-registry \
  --set containerdSocket=/run/k3s/containerd/containerd.sock
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
