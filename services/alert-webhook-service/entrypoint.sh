#!/bin/sh
set -eu

runtime_secret_dir=/tmp/alert-webhook-secrets
mkdir -p "$runtime_secret_dir"
chown app:app "$runtime_secret_dir"
chmod 0700 "$runtime_secret_dir"

copy_secret() {
    source_path=$1
    target_name=$2

    if [ -z "$source_path" ]; then
        return
    fi
    if [ ! -r "$source_path" ]; then
        echo "secret file is not readable: $target_name" >&2
        exit 1
    fi

    target_path="$runtime_secret_dir/$target_name"
    cp "$source_path" "$target_path"
    chown app:app "$target_path"
    chmod 0400 "$target_path"
}

copy_secret "${ALERT_WEBHOOK_TOKEN_FILE:-}" alert_webhook_token
copy_secret "${WECOM_WEBHOOK_URL_FILE:-}" wecom_webhook_url
copy_secret "${DINGTALK_WEBHOOK_URL_FILE:-}" dingtalk_webhook_url
copy_secret "${DINGTALK_SECRET_FILE:-}" dingtalk_secret

if [ -n "${ALERT_WEBHOOK_TOKEN_FILE:-}" ]; then
    export ALERT_WEBHOOK_TOKEN_FILE="$runtime_secret_dir/alert_webhook_token"
fi
if [ -n "${WECOM_WEBHOOK_URL_FILE:-}" ]; then
    export WECOM_WEBHOOK_URL_FILE="$runtime_secret_dir/wecom_webhook_url"
fi
if [ -n "${DINGTALK_WEBHOOK_URL_FILE:-}" ]; then
    export DINGTALK_WEBHOOK_URL_FILE="$runtime_secret_dir/dingtalk_webhook_url"
fi
if [ -n "${DINGTALK_SECRET_FILE:-}" ]; then
    export DINGTALK_SECRET_FILE="$runtime_secret_dir/dingtalk_secret"
fi

exec su-exec app:app python /app/app.py
