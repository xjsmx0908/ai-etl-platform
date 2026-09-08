# Alert Webhook Service

Internal adapter from Alertmanager webhook payloads to enterprise WeCom and
DingTalk robot messages. It uses only the Python standard library.

## Security

- Alertmanager authenticates with `Authorization: Bearer <token>`.
- The token and downstream webhook URLs are read from Docker secret files.
- The container starts as root only long enough to copy mounted secrets into a
  private, `app`-owned temporary directory, then drops privileges before
  starting the HTTP service. This preserves host secret permissions such as
  `0600` when Docker Compose bind-mounts local secret files.
- The service has no published host port in `docker-compose.yml`.
- DingTalk HMAC signing is applied when `DINGTALK_SECRET_FILE` is configured.

At least one downstream webhook must be configured. Otherwise `/alerts`
returns `503`, so Alertmanager retains and retries the failed notification
instead of silently discarding it.

`POST /notifications` accepts content-safe governance events from Query API.
It formats a Chinese markdown message for WeCom/DingTalk and, when
`WORKFLOW_ENGINE_WEBHOOK_URL` is set, also forwards the original JSON event to
an external workflow engine. If no human channel and no workflow URL are
configured, it returns `202` and skips delivery so the durable outbox can close
the event instead of retrying forever. Workflow engines submit decisions back
through `POST /v1/release-center/workflow/decision` or the existing admin
`Decide` API.

## Test

```bash
python3 -m unittest discover -s tests -p 'test_*.py' -v
docker build -t ai-etl-alert-webhook-test .
sh tests/test_entrypoint.sh ai-etl-alert-webhook-test
```
