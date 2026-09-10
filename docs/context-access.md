# Context access and consumers

PVC-backed contexts use explicit grants. A subject is a platform identity written as `kind:name`,
where kind is `user`, `agent`, `workload`, or `service`.

The creator receives `read`, `write`, `attach`, `derive`, and `administer`. No other subject can
discover or read the context until granted access. Listing is filtered, and an unauthorized direct
lookup returns `404` so it does not reveal the context's existence.

```sh
export CS_SUBJECT=user:owner
contextctl ctx create research --type state
contextctl ctx grant research --subject agent:reader --permissions read,attach
contextctl ctx grant research --subject agent:writer --permissions read,write,attach
contextctl ctx grants research
```

Set `CS_SUBJECT` to inspect the view available to another identity:

```sh
CS_SUBJECT=agent:reader contextctl ctx list --backend pvc
CS_SUBJECT=agent:reader contextctl ctx access research
```

`read` permits discovery and metadata access. `write` permits publishing revisions. `attach`
permits registering a consumer, `derive` permits creating derived context, and `administer` permits
changing grants or deleting the context. `write`, `attach`, and `derive` grants also require `read`.

## Identity boundary

Context Service authorizes the identity in `X-Context-Subject`. The CLI sends `CS_SUBJECT` through
that header. In production, an authenticating gateway must remove caller-supplied identity headers
and inject the verified subject. Context Service does not validate bearer tokens itself.

An in-cluster service account maps to `service:NAMESPACE/SERVICE_ACCOUNT`. This keeps Kubernetes
out of the public subject schema while giving the platform a deterministic mapping.

The development fallback is `user:anonymous`. Existing installations should grant their desired
subjects before requiring non-anonymous access. `service:context-service-admin` is the bootstrap
administrative identity for platform recovery.

## Consumers and safe deletion

`contextctl ctx consumers NAME` combines declared consumers with Pods currently mounting the
context PVC. It reports the intended access mode and whether the consumer is active.

Normal deletion returns `409 Conflict` while either kind of consumer exists. Remove the attachment
or Pod first. `contextctl ctx delete NAME --force` is an explicit override and may leave Kubernetes
volume deletion waiting for a Pod to release it.

Grant and consumer changes are retained as a bounded audit trail and shown with:

```sh
contextctl ctx audit NAME
```
