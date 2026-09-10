# Query agent memory and knowledge

First run the [memory](../memory-generation/) or [knowledge](../knowledge-generation/) demo. Then
query all local memory and knowledge together:

```sh
contextctl ctx query "release schedule"
```

Limit the search to selected contexts or retrieve a known record:

```sh
contextctl ctx query "release schedule" --context demo-memory --context demo-knowledge
contextctl ctx query --id RECORD_ID --json
```

Every result shows the context revision and the source context revision used to generate it.
