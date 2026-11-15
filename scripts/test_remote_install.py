"""Offline tests for scripts/remote-install.py using a temp dir and fake systemctl."""
import contextlib
import gzip
import hashlib
import importlib.util
import sqlite3
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

spec = importlib.util.spec_from_file_location(
    "remote_install", Path(__file__).with_name("remote-install.py")
)
remote_install = importlib.util.module_from_spec(spec)
spec.loader.exec_module(remote_install)


class FakeResponse:
    def __init__(self, status=200):
        self.status = status

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class RemoteInstallTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name)
        self.root = self.home / ".local/lib/smsbot"
        self.state = self.home / ".local/share/smsbot/state"
        self.root.mkdir(parents=True)
        self.state.mkdir(parents=True)

        self.old_binary = b"\x7fELF-old-smsbot"
        self.new_binary = b"\x7fELF-new-smsbot"
        (self.root / "smsbot").write_bytes(self.old_binary)
        (self.root / "smsbot-v2.gz").write_bytes(gzip.compress(self.new_binary))
        self.previous_sha = hashlib.sha256(self.old_binary).hexdigest()
        self.expected_sha = hashlib.sha256(self.new_binary).hexdigest()

        with contextlib.closing(sqlite3.connect(self.state / "smsbot.db")) as db:
            db.execute("PRAGMA user_version = 7")
            db.execute("CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)")
            db.execute("INSERT INTO settings VALUES ('paused', 'true')")
            db.commit()
        (self.state / "google-session.enc").write_bytes(b"synthetic-session")

    def stage_worker(self, label="v2", tasks=()):
        staging = self.root / f"worker-staging-{label}"
        staging.mkdir(parents=True, exist_ok=True)
        (staging / "download-worker.py").write_text("# worker v2\n", encoding="utf-8")
        (staging / "download-source.py").write_text("# source v2\n", encoding="utf-8")
        for name, content in tasks:
            (staging / name).write_text(content, encoding="utf-8")
        return staging

    def make_fake_runner(self, downloader_installed=True, downloader_restart_fails=False):
        calls = []

        def fake_run(args, **kwargs):
            calls.append(list(args))
            if len(args) >= 2 and args[0] == "systemctl" and args[1] == "--user":
                action, unit = args[2], args[3] if len(args) > 3 else ""
                if unit == "smsbot-downloader.service":
                    if action == "cat":
                        code = 0 if downloader_installed else 1
                        return subprocess.CompletedProcess(args, code, stdout=b"", stderr=b"")
                    if action == "restart" and downloader_restart_fails:
                        raise subprocess.CalledProcessError(1, args)
                return subprocess.CompletedProcess(args, 0, stdout=b"", stderr=b"")
            if len(args) >= 2 and args[1] == "--version":
                return subprocess.CompletedProcess(args, 0, stdout="v2\n", stderr="")
            raise AssertionError(f"unexpected subprocess call: {args}")

        return calls, fake_run

    def test_skips_silently_when_worker_dir_missing(self):
        staging = self.stage_worker()
        calls, fake_run = self.make_fake_runner(downloader_installed=False)
        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run), \
             mock.patch.object(remote_install.urllib.request, "urlopen", return_value=FakeResponse(200)):
            code, payload = remote_install.install_release(
                "v2", self.expected_sha, self.previous_sha, "host.ts.net", home=self.home
            )
        self.assertEqual(code, 0)
        self.assertEqual(payload["deployment"], "installed")
        self.assertFalse(payload["worker_updated"])
        self.assertFalse(payload["worker_restarted"])
        self.assertNotIn("worker_error", payload)
        self.assertFalse((self.root / "downloader").exists())
        self.assertFalse(staging.exists())
        self.assertFalse(any("smsbot-downloader.service" in c for c in calls))

    def test_updates_worker_atomically_keeps_one_backup_and_restarts(self):
        worker_dir = self.root / "downloader/worker"
        worker_dir.mkdir(parents=True)
        (worker_dir / "download-worker.py").write_text("# worker v1\n", encoding="utf-8")
        (worker_dir / "download-source.py").write_text("# source v1\n", encoding="utf-8")
        (worker_dir / "download-worker.py.before-v0-20260101T000000Z").write_text("# old backup\n", encoding="utf-8")
        (worker_dir / "task_local.py").write_text("# keep local plugin\n", encoding="utf-8")
        staging = self.stage_worker(tasks=[("task_custom.py", "# custom task\n")])
        calls, fake_run = self.make_fake_runner(downloader_installed=True)

        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run), \
             mock.patch.object(remote_install.urllib.request, "urlopen", return_value=FakeResponse(200)):
            code, payload = remote_install.install_release(
                "v2", self.expected_sha, self.previous_sha, "host.ts.net", home=self.home
            )

        self.assertEqual(code, 0)
        self.assertEqual(payload["deployment"], "installed")
        self.assertTrue(payload["worker_updated"])
        self.assertTrue(payload["worker_restarted"])
        self.assertNotIn("worker_error", payload)
        self.assertEqual((worker_dir / "download-worker.py").read_text(encoding="utf-8"), "# worker v2\n")
        self.assertEqual((worker_dir / "download-source.py").read_text(encoding="utf-8"), "# source v2\n")
        self.assertEqual((worker_dir / "task_custom.py").read_text(encoding="utf-8"), "# custom task\n")
        self.assertEqual((worker_dir / "task_local.py").read_text(encoding="utf-8"), "# keep local plugin\n")
        backups = list(worker_dir.glob("download-worker.py.before-*"))
        self.assertEqual(len(backups), 1)
        self.assertTrue(backups[0].name.startswith("download-worker.py.before-v2-"))
        self.assertEqual(backups[0].read_text(encoding="utf-8"), "# worker v1\n")
        self.assertEqual(payload["worker_backup"], backups[0].name)
        self.assertEqual(list(worker_dir.glob(".*.staged")), [])
        self.assertFalse(staging.exists())
        self.assertIn(["systemctl", "--user", "cat", "smsbot-downloader.service"], calls)
        self.assertIn(["systemctl", "--user", "restart", "smsbot-downloader.service"], calls)

    def test_updates_worker_without_restart_when_service_not_installed(self):
        worker_dir = self.root / "downloader/worker"
        worker_dir.mkdir(parents=True)
        (worker_dir / "download-worker.py").write_text("# worker v1\n", encoding="utf-8")
        self.stage_worker()
        calls, fake_run = self.make_fake_runner(downloader_installed=False)

        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run):
            result = remote_install.update_worker(self.root, "v2", "20261115T000000Z")

        self.assertTrue(result["updated"])
        self.assertFalse(result["restarted"])
        self.assertEqual((worker_dir / "download-worker.py").read_text(encoding="utf-8"), "# worker v2\n")
        self.assertIn(["systemctl", "--user", "cat", "smsbot-downloader.service"], calls)
        self.assertNotIn(["systemctl", "--user", "restart", "smsbot-downloader.service"], calls)

    def test_worker_failure_does_not_rollback_healthy_server_deploy(self):
        worker_dir = self.root / "downloader/worker"
        worker_dir.mkdir(parents=True)
        (worker_dir / "download-worker.py").write_text("# worker v1\n", encoding="utf-8")
        # Incomplete staging (missing download-source.py)
        staging = self.root / "worker-staging-v2"
        staging.mkdir(parents=True)
        (staging / "download-worker.py").write_text("# worker v2\n", encoding="utf-8")
        _, fake_run = self.make_fake_runner(downloader_installed=True)

        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run), \
             mock.patch.object(remote_install.urllib.request, "urlopen", return_value=FakeResponse(200)):
            code, payload = remote_install.install_release(
                "v2", self.expected_sha, self.previous_sha, "host.ts.net", home=self.home
            )

        self.assertEqual(code, 0)
        self.assertEqual(payload["deployment"], "installed")
        self.assertEqual((self.root / "smsbot").read_bytes(), self.new_binary)
        self.assertFalse(payload["worker_updated"])
        self.assertFalse(payload["worker_restarted"])
        self.assertEqual(payload["worker_error"], "worker_staging_incomplete")
        self.assertEqual((worker_dir / "download-worker.py").read_text(encoding="utf-8"), "# worker v1\n")

    def test_worker_restart_failure_does_not_rollback_healthy_server_deploy(self):
        worker_dir = self.root / "downloader/worker"
        worker_dir.mkdir(parents=True)
        (worker_dir / "download-worker.py").write_text("# worker v1\n", encoding="utf-8")
        self.stage_worker()
        _, fake_run = self.make_fake_runner(downloader_installed=True, downloader_restart_fails=True)

        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run), \
             mock.patch.object(remote_install.urllib.request, "urlopen", return_value=FakeResponse(200)):
            code, payload = remote_install.install_release(
                "v2", self.expected_sha, self.previous_sha, "host.ts.net", home=self.home
            )

        self.assertEqual(code, 0)
        self.assertEqual(payload["deployment"], "installed")
        self.assertEqual((self.root / "smsbot").read_bytes(), self.new_binary)
        self.assertTrue(payload["worker_updated"])
        self.assertFalse(payload["worker_restarted"])
        self.assertEqual(payload["worker_error"], "CalledProcessError")

    def test_failed_server_deploy_rolls_back_and_leaves_worker_untouched(self):
        worker_dir = self.root / "downloader/worker"
        worker_dir.mkdir(parents=True)
        (worker_dir / "download-worker.py").write_text("# worker v1\n", encoding="utf-8")
        staging = self.stage_worker()
        _, fake_run = self.make_fake_runner(downloader_installed=True)

        with mock.patch.object(remote_install.subprocess, "run", side_effect=fake_run), \
             mock.patch.object(remote_install.urllib.request, "urlopen", side_effect=OSError("down")), \
             mock.patch.object(remote_install.time, "monotonic", side_effect=[0.0, 1.0, 50.0]):
            code, payload = remote_install.install_release(
                "v2", self.expected_sha, self.previous_sha, "host.ts.net", home=self.home
            )

        self.assertEqual(code, 1)
        self.assertEqual(payload["deployment"], "failed")
        self.assertTrue(payload["restored_previous_service"])
        self.assertFalse(payload["worker_updated"])
        self.assertEqual((self.root / "smsbot").read_bytes(), self.old_binary)
        self.assertEqual((worker_dir / "download-worker.py").read_text(encoding="utf-8"), "# worker v1\n")
        self.assertFalse(staging.exists())


if __name__ == "__main__":
    unittest.main()
