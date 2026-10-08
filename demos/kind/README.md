# Kind demo

Run Context Service and several ready-made examples on a local Kubernetes cluster.

Requires Docker, Kind, `kubectl`, `curl`, and Go.

```sh
make kind-demo
export PATH="$PWD/bin:$PATH"
contextctl status
```

The demo creates several context types and mounts them in a sample agent Pod.

Clean up when finished:

```sh
make kind-demo-clean
make kind-down
```

See [Getting started](../../docs/getting-started.md) for a guided walkthrough and deployment
options.
