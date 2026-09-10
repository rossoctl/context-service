# Snapshot and clone a local context

Build `contextctl`, use a temporary context home, and create a small state context:

```sh
make build
export PATH="$PWD/bin:$PATH"
export CS_CONTEXT_HOME="$(mktemp -d)/contexts"

contextctl ctx create demo --type state --backend filesystem
mkdir -p "$CS_CONTEXT_HOME/demo/memory"
printf 'preferred editor: vim\n' > "$CS_CONTEXT_HOME/demo/memory/preferences.txt"
```

Preserve it, make a writable clone, and inspect both:

```sh
contextctl ctx snapshot create demo baseline
contextctl ctx snapshot clone demo@baseline experiment
contextctl ctx list --backend filesystem
```

Now change the original and restore it:

```sh
printf 'preferred editor: emacs\n' > "$CS_CONTEXT_HOME/demo/memory/preferences.txt"
contextctl ctx snapshot restore demo baseline
cat "$CS_CONTEXT_HOME/demo/memory/preferences.txt"
contextctl ctx snapshot list demo
```

The restore prints the name of the protected safety snapshot it created before replacing the newer
content.

Try retention without deleting anything:

```sh
contextctl ctx snapshot retention demo --keep-last 2 --max-age 168h
contextctl ctx snapshot gc demo --dry-run
```
