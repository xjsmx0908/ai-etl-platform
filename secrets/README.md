# Secrets Usage

This project supports Docker secrets with `*_FILE` variables.

## Directory Convention

- `secrets/examples/`: committed placeholders for local bootstrapping.
- `secrets/dev/`: ignored local secrets for your machine.

## Precedence

- Runtime env var (e.g. `JWT_SECRET`) has highest priority.
- Secret file var (e.g. `JWT_SECRET_FILE`) is used when env var is empty.
- Service default value is used last.

## Recommended Local Setup

1. Copy placeholders to ignored dev path:
   `cp -R secrets/examples/* secrets/dev/`
2. Replace values in `secrets/dev/*` with real local secrets.
3. Override compose file paths in `.env` if needed, for example:
   `JWT_SECRET_FILE_PATH=./secrets/dev/jwt_secret`

For enterprise alert delivery, set at least one of `wecom_webhook_url` or
`dingtalk_webhook_url`. Keep `alert_webhook_token`, webhook URLs, DingTalk
signing secret, and the Grafana admin password in `secrets/dev/` or an external
secret manager.
