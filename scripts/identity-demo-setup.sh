#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${IDENTITY_DEMO_OUTPUT_DIR:-${repo_root}/secrets/dev/identity-demo}"
template="${repo_root}/deploy/identity-demo/realm-template.json"

if ! command -v openssl >/dev/null 2>&1; then
  echo "identity demo setup requires openssl" >&2
  exit 1
fi
if ! command -v base32 >/dev/null 2>&1; then
  echo "identity demo setup requires base32" >&2
  exit 1
fi

mkdir -p "${output_dir}"
chmod 700 "${output_dir}"
umask 077

if [[ ! -s "${output_dir}/oidc-client-secret" ]]; then
  openssl rand -hex 32 >"${output_dir}/oidc-client-secret"
fi
if [[ ! -s "${output_dir}/scim-bearer-token" ]]; then
  openssl rand -hex 32 >"${output_dir}/scim-bearer-token"
fi
if [[ ! -s "${output_dir}/demo-password" ]]; then
  openssl rand -hex 16 >"${output_dir}/demo-password"
fi
if [[ ! -s "${output_dir}/keycloak-admin-password" ]]; then
  openssl rand -hex 16 >"${output_dir}/keycloak-admin-password"
fi
if [[ ! -s "${output_dir}/totp-secret" ]]; then
  openssl rand 20 | base32 | tr -d '=\r\n' >"${output_dir}/totp-secret"
  printf '\n' >>"${output_dir}/totp-secret"
fi

if [[ ! -s "${output_dir}/ca-key.pem" || ! -s "${output_dir}/ca.pem" ]]; then
  openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 30 \
    -subj "/CN=AI ETL Personal Demo CA" \
    -keyout "${output_dir}/ca-key.pem" -out "${output_dir}/ca.pem" >/dev/null 2>&1
fi

issue_certificate() {
  local name="$1"
  local dns_name="$2"
  local config="${output_dir}/${name}-openssl.cnf"
  printf '%s\n' \
    '[req]' 'distinguished_name=dn' 'prompt=no' 'req_extensions=req_ext' \
    '[dn]' "CN=${dns_name}" \
    '[req_ext]' 'subjectAltName=@alt_names' \
    '[alt_names]' "DNS.1=${dns_name}" >"${config}"
  openssl req -new -newkey rsa:2048 -nodes -sha256 -config "${config}" \
    -keyout "${output_dir}/${name}-key.pem" -out "${output_dir}/${name}.csr" >/dev/null 2>&1
  openssl x509 -req -sha256 -days 30 -in "${output_dir}/${name}.csr" \
    -CA "${output_dir}/ca.pem" -CAkey "${output_dir}/ca-key.pem" -CAcreateserial \
    -extfile "${config}" -extensions req_ext -out "${output_dir}/${name}.pem" >/dev/null 2>&1
  rm -f "${output_dir}/${name}.csr" "${config}"
}

if [[ ! -s "${output_dir}/keycloak-key.pem" || ! -s "${output_dir}/keycloak.pem" ]]; then
  issue_certificate keycloak keycloak.localhost
fi
if [[ ! -s "${output_dir}/web-key.pem" || ! -s "${output_dir}/web.pem" ]]; then
  issue_certificate web ai-etl.localhost
fi

client_secret="$(tr -d '\r\n' <"${output_dir}/oidc-client-secret")"
demo_password="$(tr -d '\r\n' <"${output_dir}/demo-password")"
totp_secret="$(tr -d '\r\n' <"${output_dir}/totp-secret")"
sed \
  -e "s/__OIDC_CLIENT_SECRET__/${client_secret}/g" \
  -e "s/__DEMO_PASSWORD__/${demo_password}/g" \
  -e "s/__TOTP_SECRET__/${totp_secret}/g" \
  "${template}" >"${output_dir}/realm.json"

chmod 600 "${output_dir}"/*
echo "Identity demo assets are ready in ${output_dir}"
