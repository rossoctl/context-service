# Deploy to agentic-cloud

This development workflow builds Context Service for the cluster's AMD64 nodes, pushes it to the private in-cluster registry, and rolls out the new image. Serverless Harness does not need to be rebuilt when only Context Service changes.

## Prerequisites

- `kubectl` is using the `agentic-cloud` context.
- Docker is running locally with `buildx` support.
- Docker can pull `quay.io/skopeo/stable` (used to push to the development registry over HTTP).
- The `cr-system/registry` service and `serverless-harness/context-service` deployment already exist.

Use a unique image tag for every deployment. The deployment uses `IfNotPresent`, so reusing a tag can leave an old image cached on a node.

## Build and deploy

In one terminal, expose the private registry only while the image is pushed:

```sh
kubectl -n cr-system port-forward service/registry 5000:5000
```

In a second terminal, from this repository:

```sh
TAG=dev-$(date +%Y%m%d-%H%M%S)

docker buildx build \
  --platform linux/amd64 \
  --tag context-service:$TAG \
  --load .

ARCHIVE="$PWD/.context-service-$TAG.tar"
docker save --output "$ARCHIVE" context-service:$TAG

docker run --rm \
  -v "$ARCHIVE:/image.tar:ro" \
  quay.io/skopeo/stable@sha256:c7d3c512612f52805023cd38351081dad7e2729fc13d14b701e47c7c8bdd6615 copy \
  --dest-tls-verify=false \
  docker-archive:/image.tar \
  docker://host.lima.internal:5000/context-service:$TAG

rm "$ARCHIVE"

kubectl -n serverless-harness set image \
  deployment/context-service \
  context-service=10.43.28.116:5000/context-service:$TAG

kubectl -n serverless-harness rollout status \
  deployment/context-service --timeout=120s

kubectl -n serverless-harness get deployment context-service \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

Stop the registry port-forward after the push finishes.

## Smoke test

Load the local CLI configuration and create a temporary pool:

```sh
set -a
source .env
set +a

NAME=deploycheck-$(date +%Y%m%d-%H%M%S)
bin/contextctl create "$NAME" --shared
bin/contextctl wait "$NAME"

kubectl -n serverless-harness get sandboxes \
  -l context.rossoctl.io/pool="$NAME"

bin/contextctl rm "$NAME"
```

New Sandbox resources should be named:

```text
sandbox-<workload-name>-<index>
```

For example, workload `bugstone-20260727-171130` creates `sandbox-bugstone-20260727-171130-0`, `-1`, and `-2`.
