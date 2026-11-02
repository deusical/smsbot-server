import importlib.util
import json
import time
import sys
import shutil
from unittest import mock
from pathlib import Path
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("download_worker", Path(__file__).with_name("download-worker.py"))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class WorkerBoundaryTests(unittest.TestCase):
    def test_async_http_boundary(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(worker.Jobs, "execute"):
            root = Path(tmp)
            token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
            server = worker.create_server(root, token, 0)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            path = "/download/jobs/" + "a"*64
            def call(value, authorization, expected, endpoint=path):
                request = urllib.request.Request(
                    "http://127.0.0.1:%s%s" % (server.server_port, endpoint),
                    data=json.dumps(value).encode(),
                    headers={"Authorization": authorization, "Content-Type": "application/json"})
                try:
                    with opener.open(request, timeout=3) as response:
                        self.assertEqual(response.status, expected)
                        self.assertEqual(response.headers["Cache-Control"], "no-store")
                        return json.loads(response.read())
                except urllib.error.HTTPError as error:
                    self.assertEqual(error.code, expected)
            try:
                valid = {"url": "https://v.douyin.com/synthetic/", "max_bytes": 19999999, "deadline": int(time.time())+900}
                call(valid, "Bearer invalid", 401)
                call({**valid, "url": "http://127.0.0.1/private"}, "Bearer " + token, 502)
                call({**valid, "max_bytes": 20000000}, "Bearer " + token, 502)
                call({**valid, "deadline": int(time.time())+7200}, "Bearer " + token, 502)
                self.assertEqual(call(valid, "Bearer " + token, 200), {"stage": "queued"})
                self.assertEqual(call(valid, "Bearer " + token, 200), {"stage": "queued"})
                call({**valid, "url": "https://xhslink.com/different"}, "Bearer " + token, 502)
                call(valid, "Bearer " + token, 502, "/download/jobs/../../private")
                self.assertEqual(worker.Jobs.execute.call_count, 1)
                self.assertNotIn(b"douyin", next(root.rglob("state.enc")).read_bytes())
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=3)

    def test_checkpoint_restart_reuses_original_and_result(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root, "synthetic-token")
            job = "b"*64
            path = jobs.path(job)
            request = {"url": "https://xhslink.com/synthetic", "max_bytes": 100000, "deadline": int(time.time())+900}
            jobs.save(path, {**request, "stage": "compressing"})
            original = root / "original.mp4"
            original.write_bytes(b"synthetic-original")
            worker.seal_file(original, path / "original.enc", jobs.key)
            original.unlink()
            # A fresh process has no in-memory state. The saved original must
            # suffice; source download is forbidden in this test.
            restarted = worker.Jobs(root, "synthetic-token")
            restarted.active[job] = time.monotonic()+30
            def compress(source, destination, limit, deadline):
                self.assertEqual(source.read_bytes(), b"synthetic-original")
                destination.write_bytes(b"synthetic-send-copy")
                return destination
            with mock.patch.object(worker, "compress", side_effect=compress), mock.patch.object(worker, "run", side_effect=AssertionError("redownloaded")):
                restarted.execute(job)
            self.assertEqual(restarted.poll(job, request), {"stage": "complete"})
            third = worker.Jobs(root, "synthetic-token")
            third.result(job, root / "result.mp4")
            self.assertEqual((root / "result.mp4").read_bytes(), b"synthetic-send-copy")
            self.assertNotIn(b"synthetic-send-copy", (path / "result.enc").read_bytes())
            self.assertNotIn(b"synthetic-original", (path / "original.enc").read_bytes())
            data = bytearray((path / "result.enc").read_bytes())
            data[14] ^= 1
            (path / "result.enc").write_bytes(data)
            with self.assertRaises(Exception):
                third.result(job, root / "tampered.mp4")

    def test_progress_stall_lease_and_absolute_deadline_kill_children(self):
        with tempfile.TemporaryDirectory() as tmp:
            worker.work.folder = Path(tmp)
            worker.work.stall_seconds = .05
            worker.work.alive = lambda: True
            try:
                with self.assertRaisesRegex(RuntimeError, "progress_stalled"):
                    worker.run([sys.executable, "-c", "import time; time.sleep(20)"], time.monotonic()+10)
                worker.work.alive = lambda: False
                with self.assertRaises(worker.LeaseExpired):
                    worker.run([sys.executable, "-c", "import time; time.sleep(20)"], time.monotonic()+10)
                worker.work.alive = lambda: True
                with self.assertRaises(TimeoutError):
                    worker.run([sys.executable, "-c", "import time; time.sleep(20)"], time.monotonic()+.05)
            finally:
                worker.work.__dict__.clear()

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg tools required")
    def test_real_synthetic_compression_and_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source.mp4"
            worker.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i",
                        "testsrc2=size=640x360:rate=24", "-f", "lavfi", "-i", "sine=frequency=400",
                        "-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "8",
                        "-c:a", "aac", str(source)], time.monotonic()+30)
            limit = source.stat().st_size // 2
            stages = []
            worker.work.folder = root
            worker.work.stage = stages.append
            worker.work.alive = lambda: True
            try:
                output = worker.compress(source, root / "send.mp4", limit, time.monotonic()+60, encoder="libx264")
                self.assertLessEqual(output.stat().st_size, limit)
                self.assertEqual(stages, ["verifying"])
                copied = worker.compress(output, root / "copied.mp4", limit, time.monotonic()+30, encoder="libx264")
                self.assertEqual(output.read_bytes(), copied.read_bytes())
            finally:
                worker.work.__dict__.clear()

    def test_url_allowlist(self):
        for url in ("https://douyin.com.attacker.test/video/1", "http://douyin.com/", "https://user:pass@douyin.com/", "https://douyin.com:443/", "file:///C:/private"):
            self.assertFalse(worker.valid_url(url))
        self.assertTrue(worker.valid_url("https://xhslink.com/synthetic"))


if __name__ == "__main__":
    unittest.main()
