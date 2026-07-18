# Alertmanager Runtime Wrapper

Docker Compose local secrets are bind-mounted host files and can retain host
ownership. The official Alertmanager image runs as `nobody`, so the runtime
wrapper copies the webhook token and configuration into an ephemeral,
`nobody`-owned directory before launching Alertmanager with `setuidgid`.

The source secret remains `0600` on the host. The copied token is scoped to the
container lifetime and is not logged.

```bash
docker build -t ai-etl-alertmanager-test .
sh test-entrypoint.sh ai-etl-alertmanager-test
```
