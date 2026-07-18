#!/bin/sh
set -eu

image=${1:-ai-etl-alert-webhook-test}
temp_dir=$(mktemp -d)
container_name="alert-webhook-entrypoint-test-$$"

cleanup() {
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    rm -rf "$temp_dir"
}
trap cleanup EXIT

printf 'test-token' > "$temp_dir/alert_webhook_token"
chmod 0600 "$temp_dir/alert_webhook_token"

docker run -d --rm \
    --name "$container_name" \
    -v "$temp_dir/alert_webhook_token:/run/secrets/alert_webhook_token:ro" \
    -e ALERT_WEBHOOK_TOKEN_FILE=/run/secrets/alert_webhook_token \
    "$image" >/dev/null

attempt=0
while [ "$attempt" -lt 20 ]; do
    if docker exec "$container_name" python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8092/healthz', timeout=1)" >/dev/null 2>&1; then
        break
    fi
    attempt=$((attempt + 1))
    sleep 0.25
done

if [ "$attempt" -eq 20 ]; then
    docker logs "$container_name" >&2
    exit 1
fi

docker exec --user app "$container_name" test -r /tmp/alert-webhook-secrets/alert_webhook_token
