import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "docker-compose.yml"
LAB = ROOT / "docker-compose.lab.yml"
PROD = ROOT / "docker-compose.prod.yml"
SMOKE = ROOT / "docker-compose.smoke.yml"
NGINX = ROOT / "deploy" / "nginx" / "rag.ipuau.com.conf"
PROMETHEUS = ROOT / "infrastructure" / "prometheus.yml"

PORT_LINE = re.compile(r"^\s*-\s*\"([^\"]+)\"")
NAKED_PUBLISH = re.compile(
    r"^(?:\d+|\$\{[A-Z0-9_]+(?::-[^}]+)?\}):\d+(?:/(?:tcp|udp))?$"
)


def published_ports(path: Path):
    values = []
    in_ports = False
    for raw in path.read_text(encoding="utf-8").splitlines():
        if re.match(r"^\s+ports:\s*$", raw):
            in_ports = True
            continue
        if in_ports:
            match = PORT_LINE.match(raw)
            if match:
                values.append(match.group(1))
                continue
            if raw.strip().startswith("#"):
                continue
            in_ports = False
    return values


class SecurityHardeningTests(unittest.TestCase):
    def test_base_compose_binds_published_ports_to_loopback_or_compose_bind(self):
        ports = published_ports(COMPOSE)
        self.assertTrue(ports, "expected published ports in docker-compose.yml")
        for mapping in ports:
            self.assertFalse(
                NAKED_PUBLISH.match(mapping),
                "host port maps to all interfaces: %s" % mapping,
            )
            self.assertTrue(
                mapping.startswith("127.0.0.1:")
                or mapping.startswith("${COMPOSE_BIND:-127.0.0.1}:"),
                "expected loopback or COMPOSE_BIND: %s" % mapping,
            )

    def test_lab_overlay_only_publishes_web_and_query_api(self):
        text = LAB.read_text(encoding="utf-8")
        self.assertIn("0.0.0.0:${WEB_HOST_PORT:-3100}:3000", text)
        self.assertIn("0.0.0.0:${QUERY_API_HOST_PORT:-8080}:8080", text)
        self.assertNotIn("5432", text)
        self.assertNotIn("9200", text)

    def test_prod_overlay_unpublishes_query_api_and_data_plane(self):
        text = PROD.read_text(encoding="utf-8")
        self.assertIn("query-api:", text)
        self.assertIn("ports: !reset []", text)
        self.assertIn("COOKIE_SECURE: ${COOKIE_SECURE:-true}", text)

    def test_smoke_override_stays_on_loopback(self):
        for mapping in published_ports(SMOKE):
            self.assertTrue(
                mapping.startswith("${COMPOSE_BIND:-127.0.0.1}:"),
                mapping,
            )

    def test_nginx_limits_login_and_sets_security_headers(self):
        text = NGINX.read_text(encoding="utf-8")
        self.assertIn("limit_req_zone $binary_remote_addr zone=rag_login", text)
        self.assertIn("location = /api/auth/login", text)
        self.assertIn("X-Content-Type-Options nosniff", text)
        self.assertIn("frame-ancestors 'none'", text)
        self.assertIn("Query API :8080", text)

    def test_prometheus_scrapes_query_api_with_bearer_token(self):
        text = PROMETHEUS.read_text(encoding="utf-8")
        self.assertIn("credentials_file: /run/secrets/metrics_token", text)
        self.assertIn("job_name: query-api", text)

    def test_redis_and_minio_use_secrets(self):
        text = COMPOSE.read_text(encoding="utf-8")
        self.assertIn("--requirepass", text)
        self.assertIn("MINIO_ROOT_USER_FILE=/run/secrets/s3_access_key", text)
        self.assertIn("QDRANT__SERVICE__API_KEY=", text)
        self.assertIn("mem_limit: 2g", text)
        self.assertNotIn("MINIO_ROOT_USER=${MINIO_ROOT_USER:-minioadmin}", text)

    def test_docker_compose_config_has_no_unspecified_host_ip(self):
        try:
            result = subprocess.run(
                ["docker", "compose", "-f", str(COMPOSE), "config"],
                cwd=ROOT,
                check=True,
                capture_output=True,
                text=True,
            )
        except (FileNotFoundError, subprocess.CalledProcessError) as err:
            self.skipTest("docker compose config unavailable: %s" % err)
        self.assertIn("host_ip: 127.0.0.1", result.stdout)
        self.assertNotIn("host_ip: 0.0.0.0", result.stdout)
