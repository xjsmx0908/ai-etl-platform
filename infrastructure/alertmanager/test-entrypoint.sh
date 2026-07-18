#!/bin/sh
set -eu

image=${1:-ai-etl-alertmanager-test}
temp_dir=$(mktemp -d)
container_name="alertmanager-entrypoint-test-$$"

cleanup() {
    docker rm -f "$container_name" >/dev/null 2>&1 || true
    rm -rf "$temp_dir"
}
trap cleanup EXIT

printf 'test-token' > "$temp_dir/alert_webhook_token"
chmod 0600 "$temp_dir/alert_webhook_token"

cat > "$temp_dir/alertmanager.yml" <<'EOF'
route:
  receiver: test-receiver
receivers:
  - name: test-receiver
    webhook_configs:
      - url: http://example.test/alerts
        http_config:
          authorization:
            type: Bearer
            credentials_file: /run/secrets/alert_webhook_token
EOF

docker run -d \
    --name "$container_name" \
    -v "$temp_dir/alert_webhook_token:/run/secrets/alert_webhook_token:ro" \
    -v "$temp_dir/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro" \
    "$image" >/dev/null

attempt=0
while [ "$attempt" -lt 20 ]; do
    if docker exec "$container_name" wget -q -O - http://127.0.0.1:9093/-/ready >/dev/null 2>&1; then
        break
    fi
    attempt=$((attempt + 1))
    sleep 0.25
done

if [ "$attempt" -eq 20 ]; then
    docker logs "$container_name" >&2
    exit 1
fi

docker exec --user nobody "$container_name" test -r /tmp/alertmanager-runtime/secrets/alert_webhook_token
docker exec --user nobody "$container_name" grep -q '^Uid:[[:space:]]*65534' /proc/1/status
