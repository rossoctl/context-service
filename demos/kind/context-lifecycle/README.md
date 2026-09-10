# CSI snapshot and clone on Kind

This integration demo runs a real producer and consumer against a CSI-backed context:

```text
producer → PVC → VolumeSnapshot → writable clone → consumer
```

```sh
make kind-lifecycle-demo
```

Kind's default `local-path` provisioner cannot snapshot volumes, so the demo installs the
Kubernetes snapshot controller and CSI hostpath test driver. It verifies that the clone contains
the saved data, excludes later source changes, and can be modified without changing the source.

The hostpath driver is for local testing only. Production clusters should use their supported CSI
driver and can inspect support with `contextctl ctx snapshot capabilities NAME --backend pvc`.

```sh
make kind-lifecycle-demo-clean
```
