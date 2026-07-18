#!/bin/sh
set -eu

source_config=/etc/alertmanager/alertmanager.yml
source_secret=/run/secrets/alert_webhook_token
runtime_dir=/tmp/alertmanager-runtime
runtime_config_dir="$runtime_dir/config"
runtime_secret_dir="$runtime_dir/secrets"
runtime_secret="$runtime_secret_dir/alert_webhook_token"
runtime_config="$runtime_config_dir/alertmanager.yml"

if [ ! -r "$source_config" ]; then
    echo "Alertmanager config is not readable" >&2
    exit 1
fi
if [ ! -r "$source_secret" ]; then
    echo "Alertmanager webhook token is not readable" >&2
    exit 1
fi

mkdir -p "$runtime_config_dir" "$runtime_secret_dir"
chown nobody:nobody "$runtime_dir" "$runtime_config_dir" "$runtime_secret_dir"
chmod 0700 "$runtime_dir" "$runtime_config_dir" "$runtime_secret_dir"

cp "$source_secret" "$runtime_secret"
chown nobody:nobody "$runtime_secret"
chmod 0400 "$runtime_secret"

sed "s|$source_secret|$runtime_secret|g" "$source_config" > "$runtime_config"
chown nobody:nobody "$runtime_config"
chmod 0400 "$runtime_config"

exec setuidgid nobody /bin/alertmanager --config.file="$runtime_config" "$@"
