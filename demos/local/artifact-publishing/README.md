# Publish and consume an artifact

Create a source workspace and a result:

```sh
contextctl ctx create build --type workspace --backend filesystem
mkdir -p /tmp/context-artifact-demo
printf 'tests: passed\n' > /tmp/context-artifact-demo/report.txt
```

Publish the result with source provenance:

```sh
contextctl ctx artifact publish results /tmp/context-artifact-demo/report.txt \
  --from build \
  --producer agent:builder

contextctl ctx artifact list results
```

Retrieve the immutable output as a consumer:

```sh
contextctl ctx artifact get results report.txt --output /tmp/consumed-report.txt
cat /tmp/consumed-report.txt
```

Change `report.txt` and publish it again to create a second version. The list shows both checksums;
append `@VERSION` to the artifact name to retrieve either one.
