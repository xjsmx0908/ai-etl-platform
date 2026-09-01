import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SETUP = ROOT / "scripts" / "identity-demo-setup.sh"
ISSUER = "https://keycloak.localhost:8443/realms/ai-etl-demo"
CALLBACK = "https://ai-etl.localhost:3443/api/auth/oidc/callback"
LOGOUT_CALLBACK = "https://ai-etl.localhost:3443/api/auth/logout/callback"
DEMO_USER_ID = "11111111-2222-4333-8444-555555555555"


class IdentityDemoAssetContractTests(unittest.TestCase):
    def test_setup_renders_importable_realm_and_private_assets(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            output = Path(temp_dir) / "identity-demo"
            env = os.environ.copy()
            env["IDENTITY_DEMO_OUTPUT_DIR"] = str(output)
            subprocess.run(["bash", str(SETUP)], cwd=ROOT, env=env, check=True)

            realm_path = output / "realm.json"
            realm = json.loads(realm_path.read_text(encoding="utf-8"))
            self.assertEqual(realm["realm"], "ai-etl-demo")
            self.assertTrue(realm["enabled"])
            self.assertEqual(realm["browserFlow"], "demo-browser-mfa")
            configs = {item["alias"]: item["config"] for item in realm["authenticatorConfig"]}
            loa_config = next(item for item in realm["authenticatorConfig"] if item["alias"] == "demo-loa-2")
            self.assertEqual(loa_config["config"], {"loa-condition-level": "2", "loa-max-age": "0"})
            self.assertEqual(configs["demo-password-amr"]["default.reference.value"], "pwd")
            self.assertEqual(configs["demo-otp-amr"]["default.reference.value"], "otp")
            level_flow = next(item for item in realm["authenticationFlows"] if item["alias"] == "demo-browser-mfa-level-2")
            executions = level_flow["authenticationExecutions"]
            self.assertEqual(
                [(item.get("authenticator"), item["requirement"]) for item in executions],
                [
                    ("conditional-level-of-authentication", "REQUIRED"),
                    ("auth-username-password-form", "REQUIRED"),
                    ("auth-otp-form", "REQUIRED"),
                ],
            )

            client = next(item for item in realm["clients"] if item["clientId"] == "rag-web")
            self.assertFalse(client["publicClient"])
            self.assertTrue(client["standardFlowEnabled"])
            self.assertEqual(client["redirectUris"], [CALLBACK])
            self.assertEqual(client["attributes"]["post.logout.redirect.uris"], LOGOUT_CALLBACK)

            user = next(item for item in realm["users"] if item["id"] == DEMO_USER_ID)
            self.assertEqual(user["username"], "demo.reader")
            self.assertEqual(user["requiredActions"], [])
            credential_types = {item.get("type") for item in user["credentials"]}
            self.assertEqual(credential_types, {"password", "otp"})
            otp = next(item for item in user["credentials"] if item["type"] == "otp")
            otp_data = json.loads(otp["credentialData"])
            self.assertEqual(otp_data["subType"], "totp")
            self.assertEqual(otp_data["secretEncoding"], "BASE32")
            self.assertTrue(json.loads(otp["secretData"])["value"])

            mapper_names = {
                mapper["protocolMapper"]
                for scope in realm["clientScopes"]
                for mapper in scope.get("protocolMappers", [])
            }
            self.assertEqual(
                mapper_names,
                {"oidc-acr-mapper", "oidc-amr-mapper", "oidc-usersessionmodel-note-mapper"},
            )
            auth_time = next(
                mapper
                for scope in realm["clientScopes"]
                for mapper in scope.get("protocolMappers", [])
                if mapper["name"] == "auth_time"
            )
            self.assertEqual(auth_time["config"]["user.session.note"], "AUTH_TIME")
            self.assertEqual(auth_time["config"]["claim.name"], "auth_time")
            self.assertEqual(auth_time["config"]["jsonType.label"], "long")

            for name in (
                "ca.pem",
                "ca-key.pem",
                "keycloak.pem",
                "keycloak-key.pem",
                "web.pem",
                "web-key.pem",
                "oidc-client-secret",
                "scim-bearer-token",
                "demo-password",
                "totp-secret",
                "keycloak-admin-password",
            ):
                asset = output / name
                self.assertTrue(asset.is_file(), name)
                self.assertEqual(stat.S_IMODE(asset.stat().st_mode), 0o600, name)

            rendered = realm_path.read_text(encoding="utf-8")
            self.assertNotIn("__OIDC_CLIENT_SECRET__", rendered)
            self.assertNotIn("__DEMO_PASSWORD__", rendered)
            self.assertNotIn("__TOTP_SECRET__", rendered)


class IdentityDemoComposeContractTests(unittest.TestCase):
    def test_compose_exposes_one_https_issuer_and_tls_web_gateway(self):
        compose = ROOT / "docker-compose.identity-demo.yml"
        result = subprocess.run(
            [
                "docker", "compose", "-f", "docker-compose.yml", "-f", "docker-compose.eval.yml",
                "-f", str(compose), "config", "--format", "json",
            ],
            cwd=ROOT,
            env={**os.environ, "IDENTITY_DEMO_ASSET_DIR": "/tmp/identity-demo-contract"},
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )
        config = json.loads(result.stdout)
        services = config["services"]

        keycloak = services["keycloak"]
        self.assertEqual(keycloak["image"], "quay.io/keycloak/keycloak:26.3.3")
        self.assertNotIn("KC_BOOTSTRAP_ADMIN_PASSWORD", keycloak["environment"])
        self.assertIn("/run/secrets/keycloak_admin_password", " ".join(keycloak["command"]))
        self.assertEqual(keycloak["ports"], [{"mode": "ingress", "host_ip": "127.0.0.1", "target": 8443, "published": "8443", "protocol": "tcp"}])
        aliases = keycloak["networks"]["default"]["aliases"]
        self.assertIn("keycloak.localhost", aliases)
        self.assertIn("/dev/tcp/127.0.0.1/9000", keycloak["healthcheck"]["test"][-1])

        query = services["query-api"]
        self.assertEqual(query["environment"]["ENVIRONMENT"], "dev")
        self.assertEqual(query["environment"]["IDENTITY_POLICY_PROFILE"], "personal-demo-v1")
        self.assertEqual(query["environment"]["SESSION_CORE_ENABLED"], "true")
        self.assertEqual(query["environment"]["OIDC_ENABLED"], "true")
        self.assertEqual(query["environment"]["OIDC_ISSUER"], ISSUER)
        self.assertEqual(query["environment"]["OIDC_REDIRECT_URI"], CALLBACK)
        self.assertEqual(query["environment"]["OIDC_LOGOUT_REDIRECT_URI"], LOGOUT_CALLBACK)
        self.assertEqual(query["environment"]["SSL_CERT_FILE"], "/run/identity-demo/ca.pem")
        self.assertEqual(query["environment"]["SCIM_ENABLED"], "true")
        self.assertEqual(query["environment"]["SCIM_ISSUER"], ISSUER)
        self.assertEqual(query["user"], "1000:1000")
        self.assertEqual(query["depends_on"]["keycloak"]["condition"], "service_healthy")
        self.assertEqual(query.get("ports", []), [])

        web = services["web"]
        self.assertEqual(web.get("ports", []), [])
        self.assertEqual(web["environment"]["COOKIE_SECURE"], "true")
        gateway = services["identity-demo-gateway"]
        self.assertEqual(gateway["ports"], [{"mode": "ingress", "host_ip": "127.0.0.1", "target": 3443, "published": "3443", "protocol": "tcp"}])
        nginx = (ROOT / "deploy" / "identity-demo" / "nginx.conf").read_text(encoding="utf-8")
        self.assertIn("proxy_set_header Host $http_host;", nginx)
        self.assertIn("proxy_set_header X-Forwarded-Host $http_host;", nginx)
        self.assertIn("proxy_set_header X-Forwarded-Port 3443;", nginx)
        self.assertIn("proxy_redirect ~^https?://[^/]+:3000(/.*)$ https://ai-etl.localhost:3443$1;", nginx)

        serialized = json.dumps(config).lower()
        for forbidden in ("insecure_skip_verify", "ssl_verify=false", "curl -k", "--insecure"):
            self.assertNotIn(forbidden, serialized)


class IdentityDemoCleanupContractTests(unittest.TestCase):
    def test_cleanup_refuses_broad_generated_asset_target(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            bin_dir = Path(temp_dir) / "bin"
            bin_dir.mkdir()
            docker = bin_dir / "docker"
            docker.write_text("#!/bin/sh\nexit 99\n", encoding="utf-8")
            docker.chmod(0o755)
            result = subprocess.run(
                ["bash", str(ROOT / "scripts" / "identity-demo-clean.sh")],
                cwd=ROOT,
                env={**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "IDENTITY_DEMO_OUTPUT_DIR": "/"},
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("refusing unsafe identity demo output directory", result.stderr)

    def test_cleanup_refuses_normalized_parent_traversal(self):
        result = subprocess.run(
            ["bash", str(ROOT / "scripts" / "identity-demo-clean.sh")],
            cwd=ROOT,
            env={**os.environ, "IDENTITY_DEMO_OUTPUT_DIR": "/tmp/identity-demo/.."},
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("refusing unsafe identity demo output directory", result.stderr)

    def test_cleanup_is_project_scoped_repeatable_and_removes_generated_assets(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = Path(temp_dir)
            bin_dir = temp / "bin"
            bin_dir.mkdir()
            calls = temp / "docker-calls"
            docker = bin_dir / "docker"
            docker.write_text(
                "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$IDENTITY_DEMO_DOCKER_CALLS\"\n",
                encoding="utf-8",
            )
            docker.chmod(0o755)
            generated = temp / "generated"
            generated.mkdir()
            (generated / "private-secret").write_text("secret", encoding="utf-8")
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "IDENTITY_DEMO_OUTPUT_DIR": str(generated),
                "IDENTITY_DEMO_DOCKER_CALLS": str(calls),
            }

            clean = ROOT / "scripts" / "identity-demo-clean.sh"
            subprocess.run(["bash", str(clean)], cwd=ROOT, env=env, check=True)
            subprocess.run(["bash", str(clean)], cwd=ROOT, env=env, check=True)

            self.assertFalse(generated.exists())
            expected = (
                "compose -p ai-etl-identity-demo -f docker-compose.yml "
                "-f docker-compose.eval.yml -f docker-compose.identity-demo.yml "
                "down -v --remove-orphans"
            )
            self.assertEqual(calls.read_text(encoding="utf-8").splitlines(), [expected, expected])


if __name__ == "__main__":
    unittest.main()
