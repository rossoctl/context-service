# Memory and knowledge queries

Agents and applications can query generated `memory` and `knowledge` contexts through one stable
contract. Results identify the context revision and original source revision; callers do not depend
on the current lexical index format.

Query local contexts:

```sh
contextctl ctx query "release schedule"
contextctl ctx query "release schedule" --context project-memory --context project-knowledge
```

Query accessible PVC contexts through Context Service:

```sh
contextctl ctx query "release schedule" --backend pvc
```

Use repeatable `--type`, `--source`, `--revision`, and `--id` filters. `--limit` bounds each page;
pass the returned `--cursor` value to continue. Empty results return an empty list. A context appears
under `unavailable` when it has no index for its current revision.

`contextctl ctx sync push` publishes the query index after transferring a memory or knowledge
context to a PVC. Access grants are enforced before any index is read.

The initial implementation is deterministic lexical search. The request and response types do not
expose that detail, allowing a later vector or hybrid backend without changing callers.
