# Kind demo

Run Context Service and several ready-made examples on a local Kubernetes cluster.

Requires Docker, Kind, `kubectl`, `curl`, and Go.

```sh
make kind-demo
export PATH="$PWD/bin:$PATH"
contextctl status
```

The demo includes multiple context types plus shared, dedicated, and read-only sandbox
workspaces.

Clean up when finished:

```sh
make kind-demo-clean
make kind-down
```

See [Getting started](../../docs/getting-started.md) for a guided walkthrough, deployment options,
sandbox profiles, and workspace layouts.
