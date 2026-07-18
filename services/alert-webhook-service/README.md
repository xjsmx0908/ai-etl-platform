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

## Test

```bash
python3 -m unittest discover -s tests -p 'test_*.py' -v
docker build -t ai-etl-alert-webhook-test .
sh tests/test_entrypoint.sh ai-etl-alert-webhook-test
```
