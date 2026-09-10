# Context portability

Export captured agent state as a portable file or synchronize it with a PVC-backed context.

## Before you begin

Build the CLI and create a captured filesystem context named `demo`. The
[local agent state capture demo](../harness-capture/) shows the complete flow.

```sh
make build
export PATH="$PWD/bin:$PATH"
contextctl ctx get demo --backend filesystem
```

## Move a context as a file

Export a checksummed `.context` bundle:

```sh
contextctl ctx export demo
```

Copy `demo.context` to another machine, then import it:

```sh
contextctl ctx import demo.context --name demo-copy
contextctl ctx get demo-copy --backend filesystem
```

## Synchronize with Context Service

The current `kubectl` context and `CS_URL` must refer to the same cluster.

```sh
contextctl ctx create cloud-demo --type state
contextctl ctx sync push demo --remote-name cloud-demo
contextctl ctx sync pull cloud-demo --name downloaded-demo
contextctl ctx get downloaded-demo --backend filesystem
```

Push publishes a checksummed revision on the PVC. Pull verifies and imports the current revision;
it does not overwrite an existing local context.

See [Context portability](../../../docs/context-portability.md) for the bundle format and PVC sync
design.
