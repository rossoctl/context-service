# Getting started

This guide covers local setup, the example demo, and deployment.

## Guided Kind quickstart

Install Docker, Kind, `kubectl`, `curl`, and Go first.

### Build `contextctl`

```sh
make build
export PATH="$PWD/bin:$PATH"
```

### Start Context Service

```sh
make kind-up
export CS_STORAGE_CLASS=local-path

contextctl sc list
```

### Create a context

```sh
contextctl ctx create demo
contextctl ctx list
```

A new context may show `provisioning` until a workload mounts it. Mount the PVC named in its
`attachment.claimName` in your own Pod or sandbox; Context Service does not create execution
environments.

### See what was created

```sh
contextctl status

# Refresh continuously
watch contextctl status
```

### Clean up

```sh
contextctl ctx delete demo
make kind-down
```

Context Service runs at `http://127.0.0.1:8080`. Run `make kind-smoke` to test the context
lifecycle automatically.

## Ready-made demo

Create several ready-to-explore examples and display them together:

```sh
make kind-demo
export PATH="$PWD/bin:$PATH"
contextctl status
```

The demo includes:

- `demo-workspace`, `demo-memory`, and `demo-artifacts` contexts mounted by a sample agent Pod

```sh
make kind-demo-clean
make kind-down
```

## Deploy to Kubernetes

```sh
kubectl apply -f deploy/context-service.yaml
kubectl -n context-service rollout status deployment/context-service
```

Before production, update the namespace and Context Service image in
[`deploy/context-service.yaml`](../deploy/context-service.yaml).

If OpenShift `restricted-v2` rejects UID `65532`, remove `runAsUser` and `runAsGroup` from the
Deployment so OpenShift can assign them.

## CLI

The resource aliases `ctx` and `sc` are available for interactive use. For example,
`contextctl ctx list` is equivalent to `contextctl context list`.

Copy and edit the example configuration, then load it into your shell:

```sh
cp .env.example .env
# Edit .env and replace the token placeholder.
set -a
source .env
set +a
```

The local `.env` contains credentials and is ignored by Git. `.env.example` is safe to commit.

Run `contextctl help` or `contextctl help context` for all options and examples.
