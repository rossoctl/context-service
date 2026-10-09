# How context uploads work

Moca authenticates the user and runs the workload. The client uploads context bytes directly to
Context Service. Context Service stores each upload as one content-addressed revision in the Context
PVC and streams the frozen revision back to Moca as a portable bundle. Context Service never creates
Sandboxes, and Moca never mounts the Context PVC.

![Context upload and bundle export flow](context-upload.svg)

## Flow

1. Moca creates a Context for the user with `POST /internal/v1/contexts`.
2. Moca requests an upload capability and gives the client its `uploadUrl` and `token`.
3. The client sends the `.context` bundle with `PUT /v1/uploads/{id}`.
4. A short-lived helper Pod mounts the Context PVC and materializes the bundle in
   `.context-service/materialized/<revision>`. It checks the archive structure, Context type,
   paths, checksums, and size limits, and it rejects symbolic links and unsafe paths.
5. Context Service deletes the helper Pod and publishes the revision.
6. Moca freezes that exact revision. Later uploads and publications are rejected.
7. Moca downloads the frozen revision. A read-only helper Pod streams the bundle through Context
   Service without buffering it.
8. Moca deletes the Context when the session no longer needs it.

## Safety boundaries

- Only Moca holds `CS_CONTROL_PLANE_TOKEN`. Store it in the `context-service-control-plane` Secret.
- The client receives a capability, not the service token or PVC name.
- A capability is bound to one Context and its PVC UID, so a recreated PVC invalidates it.
- A capability succeeds once and expires after five minutes. A failed attempt can retry until it
  expires. Concurrent redemption is rejected. Grant revocation does not cancel an issued capability.
- Uploads to one PVC run one at a time, and freeze fails while an upload is in progress.
- Compressed bundles are limited to 256 MiB. Expanded content is limited to 1 GiB or 90 percent of
  the PVC, whichever is smaller.
- Export requires the service bearer, a delegated subject, `read` access, and a revision that is
  both current and frozen on the same PVC UID. Context Service checks the UID again after the helper
  starts.
- Materialized directories use `0755`. Materialized files use `0644`.
- Helper Pods run as non-root without a service-account token, and they use the image digest of the
  running Context Service, or `CS_UPLOAD_IMAGE`.

## Limits of this prototype

Capabilities and upload locks live in memory. Run one Context Service replica. A restart invalidates
outstanding capabilities.
