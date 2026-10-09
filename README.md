# containerd-registry Helm repository

This branch holds nothing but the Helm chart index, which the release workflow
updates on every stable release; the source lives on `main`.

```sh
helm repo add containerd-registry https://ruakij.github.io/containerd-registry
helm repo update
helm install containerd-registry containerd-registry/containerd-registry -n kube-system
```
