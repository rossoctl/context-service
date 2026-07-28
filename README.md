# Context Service

Context Service creates sandbox pools with durable workspaces for agent workloads.

The first integration target is serverless-harness: it requests a pool at run time, receives a Kubernetes selector, and routes work to the resulting sandboxes.

Read the long-term [vision](VISION.md) and the short [design and workflow](docs/design.md).

For the current development cluster, see [deploying to agentic-cloud](docs/deploy-agentic-cloud.md).

Status: early prototype. The API is not stable.

## CLI

Build the small command-line client:

```sh
make build
```

Copy and edit the example configuration, then load it into your shell:

```sh
cp .env.example .env
# Edit .env and replace the token placeholder.
set -a
source .env
set +a
```

The local `.env` contains credentials and is ignored by Git. `.env.example` is safe to commit and documents every setting.

Create a two-sandbox pool with a shared workspace:

```sh
bin/contextctl create demo --shared
bin/contextctl wait demo
bin/contextctl get demo
bin/contextctl rm demo
```

A single-sandbox pool needs no options:

```sh
bin/contextctl create demo
```

Run `bin/contextctl help` or `bin/contextctl help create` for defaults, options, and examples.
