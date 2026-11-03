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


class ProtocolV2Tests(unittest.TestCase):
    def test_parsing_and_bounds(self):
        self.assertEqual(worker.protocol_settings({"protocol": 2, "settings": {}}), worker.DEFAULT_SETTINGS)
        for key, bad in (("preset", "unsafe"), ("preset", None), ("passes", 0), ("passes", 3),
                         ("passes", True), ("max_attempts", 0), ("max_attempts", 4),
                         ("copy_if_fits", 1), ("max_mb", .01), ("max_mb", 20),
                         ("max_mb", float("nan")), ("execution_minutes", 0), ("execution_minutes", 61),
                         ("encoder", "libx264")):
            with self.subTest(key=key, bad=bad), self.assertRaises(ValueError):
                worker.protocol_settings({"protocol": 2, "settings": {key: bad}})
        with self.assertRaises(ValueError):
            worker.protocol_settings({"protocol": 3, "settings": {}})

    def test_http_v2_and_execution_deadline(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(worker.Jobs, "execute"):
            token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
            server = worker.create_server(Path(tmp), token, 0)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            base = "http://127.0.0.1:%s/download" % server.server_port
            try:
                req = urllib.request.Request(base + "/capabilities", headers={"Authorization": "Bearer " + token})
                with opener.open(req) as response:
                    self.assertEqual(json.loads(response.read()), {"protocol": 2})
                value = {"url": "https://douyin.com/synthetic", "max_bytes": 100000,
                         "deadline": int(time.time())+600, "protocol": 2, "settings": {"execution_minutes": 1}}
                def call(value):
                    req = urllib.request.Request(base + "/jobs/" + "e"*64, data=json.dumps(value).encode(),
                        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                    return opener.open(req)
                with self.assertRaises(urllib.error.HTTPError) as error:
                    call(value)
                self.assertEqual(error.exception.code, 502)
                value["settings"] = {"execution_minutes": 10}
                with call(value) as response:
                    self.assertEqual(json.loads(response.read()), {"stage": "queued", "protocol": 2})
            finally:
                server.shutdown(); server.server_close(); thread.join(timeout=3)

    def test_checkpoint_resume_uses_persisted_settings(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); jobs = worker.Jobs(root, "synthetic-token"); job = "c"*64
            settings = {**worker.DEFAULT_SETTINGS, "preset": "fast", "passes": 1, "max_attempts": 2,
                        "copy_if_fits": False, "max_mb": 1, "execution_minutes": 5}
            request = {"url": "https://xhslink.com/synthetic", "max_bytes": 500000,
                       "deadline": int(time.time())+300, "protocol": 2, "settings": settings}
            with mock.patch.object(worker.threading, "Thread"):
                jobs.poll(job, request)
            path = jobs.path(job)
            effective = {**settings, "max_mb": .5}
            self.assertEqual(jobs.load(path)["settings"], effective)
            source = root / "original.mp4"; source.write_bytes(b"synthetic-original")
            worker.seal_file(source, path / "original.enc", jobs.key); source.unlink()
            restarted = worker.Jobs(root, "synthetic-token")
            restarted.active[job] = time.monotonic()+30
            def compress(source, destination, limit, deadline, settings):
                self.assertEqual(settings, effective)
                destination.write_bytes(b"synthetic-result"); return destination
            with mock.patch.dict(worker.DEFAULT_SETTINGS, {"preset": "veryfast", "passes": 2}), \
                 mock.patch.object(worker, "compress", side_effect=compress), \
                 mock.patch.object(worker, "run", side_effect=AssertionError("redownloaded")):
                restarted.execute(job)
            self.assertEqual(restarted.load(path)["stage"], "complete")
            self.assertEqual(restarted.load(path)["settings"], effective)
            with self.assertRaises(ValueError):
                restarted.poll(job, {**request, "settings": {**settings, "preset": "slow"}})

    def encode(self, settings, encoder="libx264", source_size=200000, sizes=None):
        folder = tempfile.TemporaryDirectory(); self.addCleanup(folder.cleanup)
        root = Path(folder.name); source = root / "original.mp4"; source.write_bytes(b"x"*source_size)
        calls = []; sizes = iter(sizes or [10]*3)
        info = {"format": {"duration": "1"}, "streams": [{"codec_type": "video"}, {"codec_type": "audio"}]}
        def run(args, deadline, **kwargs):
            calls.append(args)
            if "-c:v" in args and args[-1].endswith(".mp4"):
                Path(args[-1]).write_bytes(b"x"*next(sizes))
        with mock.patch.object(worker, "probe", return_value=info), mock.patch.object(worker, "run", side_effect=run):
            worker.compress(source, root / "send.mp4", 500000, time.monotonic()+60, encoder=encoder,
                            settings={**worker.DEFAULT_SETTINGS, "max_mb": .1, **settings})
        return calls

    def test_each_preset_and_passes_map_to_ffmpeg(self):
        for preset, nvenc in (("veryfast", "p3"), ("faster", "p4"), ("fast", "p5"), ("medium", "p6"), ("slow", "p7")):
            for encoder in ("libx264", "av1_nvenc"):
                for passes in (1, 2):
                    calls = self.encode({"preset": preset, "passes": passes}, encoder)
                    encodes = [args for args in calls if "-c:v" in args]
                    self.assertEqual(len(encodes), passes if encoder == "libx264" else 1)
                    for args in encodes:
                        self.assertEqual(args[args.index("-preset")+1], preset if encoder == "libx264" else nvenc)
                        if encoder == "libx264":
                            self.assertEqual(args[args.index("-threads")+1], "2")
                            self.assertEqual("-pass" in args, passes == 2)
                        else:
                            self.assertEqual(args[args.index("-multipass")+1], "fullres" if passes == 2 else "disabled")
                    self.assertEqual(sum("-xerror" in args for args in calls), 1)

    def test_copy_attempts_and_max_mb(self):
        self.assertFalse(any("-c:v" in args for args in self.encode({}, source_size=10)))
        self.assertTrue(any("-c:v" in args for args in self.encode({"copy_if_fits": False}, source_size=10)))
        for encoder in ("libx264", "av1_nvenc"):
            with self.assertRaisesRegex(ValueError, "size_limit"):
                self.encode({"max_attempts": 1}, encoder, sizes=[200000])
            calls = self.encode({"max_attempts": 2}, encoder, sizes=[200000, 10])
            self.assertEqual(sum("-c:v" in args and args[-1].endswith(".mp4") for args in calls), 2)
        small = self.encode({"max_mb": .1, "passes": 1, "copy_if_fits": False})
        large = self.encode({"max_mb": .2, "passes": 1, "copy_if_fits": False})
        def bitrate(calls):
            args = next(args for args in calls if "-b:v" in args)
            return int(args[args.index("-b:v")+1])
        self.assertGreater(bitrate(large), bitrate(small))


if __name__ == "__main__":
    unittest.main()
