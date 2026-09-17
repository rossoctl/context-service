# Local harness to a shared PVC

This showcase captures native state from a local agent harness, transfers it to a Context Service
PVC, keeps it backed up, and restores it into another local project. The PVC can be backed by IBM
Storage Scale or any other CSI provider.

## Before you begin

Build `contextctl`, configure access to a running Context Service, and select its Kubernetes
cluster. `CS_URL` (and `CS_TOKEN` when required) and the current `kubectl` context must refer to the
same cluster. Set the storage class for the PVC you want to demonstrate:

```sh
make build
export PATH="$PWD/bin:$PATH"
export CS_NAMESPACE=serverless-harness
export CS_STORAGE_CLASS=ibm-scale-csi
```

## Capture a real session

```sh
mkdir -p /tmp/context-demo
cd /tmp/context-demo
contextctl ctx create demo --type state --backend filesystem
contextctl ctx attach demo --harness claude
claude
```

Tell Claude: `Remember that this project's codename is Juniper.` Wait for the response, then exit.
Context Service captures the native session automatically.

```sh
contextctl ctx get demo --backend filesystem
```

## Push on demand

Create a PVC-backed destination and transfer the current revision:

```sh
contextctl ctx create cloud-demo --type state
contextctl ctx sync push demo --remote-name cloud-demo
```

The transfer streams through Kubernetes, verifies its checksum, and atomically publishes the new
revision on the PVC.

## Keep later changes backed up

```sh
contextctl ctx backup start demo --to pvc://serverless-harness/cloud-demo
claude --continue
```

Give Claude one more durable fact and exit. The harness hook captures the response and wakes the
background backup worker.

```sh
contextctl ctx backup status demo
contextctl ctx graph
```

`backup status` shows the latest verified revision and transfer statistics. `graph` shows the local
harness attachment and PVC copy in one view.

## Pull and restore elsewhere

Pull the current PVC revision into a new local context:

```sh
contextctl ctx sync pull cloud-demo --name recovered-demo
mkdir -p /tmp/context-demo-recovered
contextctl ctx restore recovered-demo --harness claude --project /tmp/context-demo-recovered
cd /tmp/context-demo-recovered
claude --resume
```

Select the restored session and ask: `What is the project codename?`

## Clean up

```sh
contextctl ctx backup stop demo
contextctl ctx detach demo --harness claude --project /tmp/context-demo
contextctl ctx delete cloud-demo
rm -rf "$HOME/.contexts/demo" "$HOME/.contexts/recovered-demo"
```

See [Harness attachments](../../../docs/harness-attachments.md),
[Context portability](../../../docs/context-portability.md), and
[Automatic context backup](../../../docs/context-backup.md) for implementation details and safety
behavior.
