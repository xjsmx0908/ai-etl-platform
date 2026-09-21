import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ROOT / "scripts"


def load_script(name: str):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), SCRIPTS / name)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class BackupStackContractTests(unittest.TestCase):
    def setUp(self):
        self.script = (SCRIPTS / "backup-stack.sh").read_text(encoding="utf-8")

    def test_backs_up_authoritative_state_with_the_right_tools(self):
        # PostgreSQL is the authority; a logical dump is the only portable form.
        self.assertIn("pg_dump", self.script)
        self.assertIn("-Fc", self.script)
        # Source objects are archived from the volume, mounted read-only so a
        # backup cannot modify what it is backing up.
        self.assertIn("minio-data.tar", self.script)
        self.assertIn(":/data:ro", self.script)

    def test_backs_up_projections_even_though_they_are_rebuildable(self):
        # Re-embedding calls an endpoint outside the stack, so "rebuildable" does
        # not survive the loss of the host. ADR-0010 then requires snapshots.
        self.assertIn("/snapshots", self.script)
        self.assertIn("es-index-archive.py dump", self.script)

    def test_never_destroys_the_stack_it_is_protecting(self):
        self.assertNotIn("down -v", self.script)
        self.assertNotIn("docker volume rm", self.script)

    def test_records_integrity_and_rotates(self):
        self.assertIn("backup-manifest.py", self.script)
        self.assertIn("integrity", self.script)
        self.assertIn("RETENTION", self.script)
        self.assertIn("latest", self.script)
        # Consecutive failures have to be discoverable, not just logged once.
        self.assertIn("consecutive_failures", self.script)


class RestoreStackContractTests(unittest.TestCase):
    def setUp(self):
        self.script = (SCRIPTS / "restore-stack.sh").read_text(encoding="utf-8")

    def test_refuses_to_run_against_a_stack_it_could_delete(self):
        self.assertIn('!= *restore*', self.script)
        self.assertIn("ai-etl-platform", self.script)
        self.assertIn("ai-etl-smoke", self.script)

    def test_runs_in_an_isolated_project_and_cleans_up_its_own_volumes(self):
        self.assertIn("COMPOSE_PROJECT_NAME=", self.script)
        self.assertIn("docker-compose.eval.yml", self.script)
        self.assertIn("docker-compose.restore.yml", self.script)
        self.assertIn("down -v --remove-orphans", self.script)

    def test_restores_authoritative_state_before_starting_the_application(self):
        restore_order = [
            self.script.index("pg_restore"),
            self.script.index("minio-data.tar -C /data"),
            self.script.index("snapshots/upload"),
            self.script.index("es-index-archive.py restore"),
            self.script.index("docker compose up -d query-api"),
        ]
        self.assertEqual(restore_order, sorted(restore_order))

    def test_asserts_boot_convergence_so_the_seed_cannot_fake_the_restore(self):
        self.assertIn("restored-counts-before-boot.json", self.script)
        self.assertIn("restored-counts.json", self.script)
        self.assertIn("boot_convergence", self.script)


class RestoreOverlayTests(unittest.TestCase):
    def test_overlay_reexposes_only_dedicated_ports(self):
        overlay = (ROOT / "docker-compose.restore.yml").read_text(encoding="utf-8")
        for port in ("55432", "6335", "9202", "9002", "8082"):
            self.assertIn(port, overlay)
        # `!override` keeps the resolved port list deterministic instead of
        # appending to whatever the eval overlay reset.
        self.assertEqual(overlay.count("!override"), 5)


class BackupManifestTests(unittest.TestCase):
    def _run(self, backup_dir: Path, inventory: dict, catalog_keys: list[str], extra=None):
        (backup_dir / "postgres.dump").write_bytes(b"dump")
        (backup_dir / "object-inventory.json").write_text(json.dumps(inventory), encoding="utf-8")
        (backup_dir / "catalog-object-keys.txt").write_text(
            "\n".join(catalog_keys) + "\n", encoding="utf-8"
        )
        (backup_dir / "catalog-counts.json").write_text(
            json.dumps({"documents": len(catalog_keys)}), encoding="utf-8"
        )
        command = [
            sys.executable,
            str(SCRIPTS / "backup-manifest.py"),
            "--dir", str(backup_dir),
            "--project", "ai-etl-platform",
            "--catalog-keys", str(backup_dir / "catalog-object-keys.txt"),
            "--catalog-counts", str(backup_dir / "catalog-counts.json"),
            "--inventory", str(backup_dir / "object-inventory.json"),
            "--component", "postgres=postgres.dump",
        ]
        command.extend(extra or [])
        return subprocess.run(command, capture_output=True, text=True)

    def test_reports_ok_when_every_referenced_object_is_present(self):
        with tempfile.TemporaryDirectory() as tmp:
            backup_dir = Path(tmp)
            result = self._run(
                backup_dir,
                {"endpoint": "http://127.0.0.1:9000", "bucket": "documents",
                 "total": 2, "bytes": 20, "objects": {"a.md": 10, "b.md": 10}},
                ["a.md", "b.md"],
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = json.loads((backup_dir / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["integrity"]["status"], "ok")
            self.assertEqual(manifest["integrity"]["missing_objects"], 0)
            self.assertEqual(manifest["catalog"]["documents_with_object_key"], 2)

    def test_reports_degraded_when_the_catalog_outlives_its_objects(self):
        with tempfile.TemporaryDirectory() as tmp:
            backup_dir = Path(tmp)
            result = self._run(
                backup_dir,
                {"endpoint": "http://127.0.0.1:9000", "bucket": "documents",
                 "total": 0, "bytes": 0, "objects": {}},
                ["a.md", "b.md"],
                extra=["--require-integrity"],
            )
            # The whole point of the gate: a backup that captured a catalog with
            # no objects behind it must not report success.
            self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
            manifest = json.loads((backup_dir / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["integrity"]["status"], "degraded")
            self.assertEqual(manifest["integrity"]["missing_objects"], 2)

    def test_degraded_alone_does_not_fail_the_backup(self):
        with tempfile.TemporaryDirectory() as tmp:
            backup_dir = Path(tmp)
            result = self._run(
                backup_dir,
                {"endpoint": "http://127.0.0.1:9000", "bucket": "documents",
                 "total": 0, "bytes": 0, "objects": {}},
                ["a.md"],
            )
            # The artifacts are still worth keeping; the verdict is data, and
            # --require-integrity is what turns it into a failure.
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = json.loads((backup_dir / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["integrity"]["status"], "degraded")

    def test_missing_artifact_is_a_hard_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            backup_dir = Path(tmp)
            (backup_dir / "object-inventory.json").write_text(
                json.dumps({"objects": {}}), encoding="utf-8"
            )
            result = subprocess.run(
                [
                    sys.executable, str(SCRIPTS / "backup-manifest.py"),
                    "--dir", str(backup_dir),
                    "--project", "ai-etl-platform",
                    "--inventory", str(backup_dir / "object-inventory.json"),
                    "--component", "postgres=absent.dump",
                ],
                capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 2)


class RestoreDrillTests(unittest.TestCase):
    def test_citation_objects_are_normalized_to_document_ids(self):
        module = load_script("restore-drill.py")
        citations = [
            {"chunk_id": "demo-doc-handbook-1", "doc_id": "demo-doc-handbook"},
            {"chunk_id": "demo-doc-onboarding-2", "doc_id": "demo-doc-onboarding"},
            "demo-doc-payroll",
            {"chunk_id": "legacy-7"},
        ]
        self.assertEqual(
            module._citation_ids(citations),
            ["demo-doc-handbook", "demo-doc-onboarding", "demo-doc-payroll", "legacy"],
        )

    def test_referential_fidelity_is_separate_from_environment_health(self):
        source = (SCRIPTS / "restore-drill.py").read_text(encoding="utf-8")
        # The drill must be able to pass on a stack that is faithfully restored
        # from an already-degraded source, and fail when asked to be stricter.
        self.assertIn("missing_before", source)
        self.assertIn("require_intact", source)


if __name__ == "__main__":
    unittest.main()
