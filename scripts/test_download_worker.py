import importlib.util
import json
import os
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
                valid = {"protocol": 4, "task": "video.download", "version": 1, "scope": "c"*64,
                         "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}},
                         "settings": {}, "deadline": int(time.time())+900}
                call(valid, "Bearer invalid", 401)
                call({**valid, "protocol": 2}, "Bearer " + token, 502)
                bad = {"protocol": 4, "task": "video.download", "version": 1, "scope": "c"*64,
                       "inputs": {"url": {"type": "url", "text": "http://127.0.0.1/private"}},
                       "settings": {}, "deadline": valid["deadline"]}
                call(bad, "Bearer " + token, 502)
                call({**valid, "deadline": int(time.time())+25*3600}, "Bearer " + token, 502)
                huge = {"protocol": 4, "task": "video.compress", "version": 1, "scope": "c"*64,
                        "inputs": {"source": {"handle": "d"*64+"/original"}},
                        "settings": {"max_bytes": 20000000}, "deadline": valid["deadline"]}
                call(huge, "Bearer " + token, 502)
                self.assertEqual(call(valid, "Bearer " + token, 200), {"stage": "queued", "protocol": 4})
                self.assertEqual(call(valid, "Bearer " + token, 200), {"stage": "queued", "protocol": 4})
                changed = {**valid, "inputs": {"url": {"type": "url", "text": "https://xhslink.com/different"}}}
                call(changed, "Bearer " + token, 502)
                call(valid, "Bearer " + token, 502, "/download/jobs/../../private")
                self.assertEqual(worker.Jobs.execute.call_count, 1)
                saved = json.loads(next(root.rglob("state.json")).read_text())
                self.assertEqual(saved["inputs"]["url"]["text"], "https://v.douyin.com/synthetic/")
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=3)

    def test_checkpoint_restart_reuses_saved_original(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root)
            job = "b"*64
            path = jobs.path(job)
            (path / "files").mkdir()
            (path / "files" / "original").write_bytes(b"synthetic-original")
            request = {"protocol": 4, "task": "video.download", "version": 1, "scope": "c"*64,
                       "inputs": {"url": {"type": "url", "text": "https://xhslink.com/synthetic"}},
                       "settings": {}, "deadline": int(time.time())+900, "stage": "downloading"}
            jobs.save(path, request)
            # A fresh process has no in-memory state. The saved original must
            # suffice; fetching the link again is forbidden in this test.
            restarted = worker.Jobs(root)
            restarted.active[job] = time.monotonic()+30
            with mock.patch.object(worker, "fetch_original", side_effect=AssertionError("redownloaded")):
                restarted.execute(job)
            state = restarted.load(path)
            self.assertEqual(state["stage"], "complete")
            self.assertEqual((path / "files" / "original").read_bytes(), b"synthetic-original")
            self.assertEqual(state["outputs"]["original"]["handle"], job + "/original")

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

    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "FFmpeg tools required")
    def test_encoded_copies_cap_the_long_side_and_keep_the_shape(self):
        # Landscape and portrait sources above the cap shrink to it; a small
        # odd-sized one is never upscaled, only rounded to even dimensions.
        for size, want in (("2400x1350", (1920, 1080)), ("1350x2400", (1080, 1920)), ("641x361", (640, 360))):
            with self.subTest(size=size), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / "source.mp4"
                worker.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i",
                            f"testsrc2=size={size}:rate=10", "-t", "1", "-c:v", "libx264", "-preset", "ultrafast",
                            "-crf", "8", "-pix_fmt", "yuv444p", str(source)], time.monotonic()+60)
                output = worker.compress(source, root / "send.mp4", source.stat().st_size // 2, time.monotonic()+120,
                                         encoder="libx264", settings={"passes": 1, "copy_if_fits": False})
                stream = next(s for s in worker.probe(output, time.monotonic()+30)["streams"] if s["codec_type"] == "video")
                self.assertEqual((stream["width"], stream["height"]), want)

    def test_bind_only_loopback_or_tailscale(self):
        for good in ("127.0.0.1", "100.92.5.20", "100.64.0.1", "100.127.255.254"):
            self.assertEqual(worker.bind_address(good), good)
        # Every interface, a LAN address, IPv6 and names are refused: plain HTTP
        # there would expose the bearer token to the network.
        for bad in ("0.0.0.0", "192.168.1.20", "10.0.0.5", "100.128.0.1", "100.63.255.255", "::1", "lenovo", ""):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                worker.bind_address(bad)
        with self.assertRaises(ValueError):
            worker.create_server(Path(tempfile.gettempdir()), "x"*40, 0, bind="0.0.0.0")

    def test_url_allowlist(self):
        for url in ("https://douyin.com.attacker.test/video/1", "http://douyin.com/", "https://user:pass@douyin.com/", "https://douyin.com:443/", "file:///C:/private"):
            self.assertFalse(worker.valid_url(url))
        self.assertTrue(worker.valid_url("https://xhslink.com/synthetic"))


PLUGIN = '''
def validate(inputs, settings):
    if set(inputs) - {"text", "file"} or set(settings) - {"repeat"}:
        raise ValueError("invalid_request")
    return inputs, {"repeat": int(settings.get("repeat", 1))}

def run(job):
    if job.inputs.get("text", {}).get("text") == "fail":
        job.fail("too_long")
    if job.inputs.get("text", {}).get("text") == "crash":
        raise RuntimeError("boom")
    job.stage("echoing")
    if "file" in job.inputs:
        data = job.file(job.inputs["file"]).read_bytes()
    else:
        data = job.inputs["text"]["text"].encode()
    out = job.folder / "echo.bin"
    out.write_bytes(data * job.settings["repeat"])
    job.output("length", {"type": "number", "text": str(len(data))})
    job.output_file("echo", out, "file")

TASKS = {"test.echo": {"version": 2, "validate": validate, "run": run}}
'''


class ProtocolV4Tests(unittest.TestCase):
    scope = "f"*64

    def jobs(self):
        folder = tempfile.TemporaryDirectory(); self.addCleanup(folder.cleanup)
        root = Path(folder.name)
        (root / "task_echo.py").write_text(PLUGIN)
        return root, worker.Jobs(root, tasks=worker.load_tasks(root))

    def request(self, **extra):
        return {"protocol": 4, "task": "test.echo", "version": 2, "scope": self.scope,
                "inputs": {"text": {"type": "text", "text": "hi"}}, "settings": {"repeat": 3},
                "deadline": int(time.time())+600, **extra}

    def run_job(self, jobs, job, request):
        with mock.patch.object(worker.threading, "Thread"):
            first = jobs.poll(job, request)
        self.assertEqual(first, {"stage": "queued", "protocol": 4})
        jobs.active[job] = time.monotonic()+30
        jobs.execute(job)
        return jobs.poll(job, request)

    def test_plugins_load_and_are_checked(self):
        root, jobs = self.jobs()
        self.assertEqual({n: t["version"] for n, t in jobs.tasks.items()},
                         {"video.download": 1, "video.compress": 1, "test.echo": 2})
        (root / "task_bad.py").write_text('TASKS = {"Bad Name": {"version": 1, "run": print}}')
        with self.assertRaises(SystemExit):
            worker.load_tasks(root)
        (root / "task_bad.py").write_text('TASKS = {"video.download": {"version": 9, "run": print}}')
        with self.assertRaises(SystemExit):
            worker.load_tasks(root)

    def test_task_outputs_values_and_files_and_chains_within_a_scope(self):
        root, jobs = self.jobs()
        first, second = "1"*64, "2"*64
        done = self.run_job(jobs, first, self.request())
        self.assertEqual(done["stage"], "complete")
        self.assertEqual(done["outputs"]["length"], {"type": "number", "text": "2"})
        self.assertEqual(done["outputs"]["echo"], {"type": "file", "handle": first + "/echo", "size": 6})
        self.assertEqual(jobs.output_file(first, "echo").read_bytes(), b"hihihi")
        with self.assertRaises(ValueError):
            jobs.output_file(first, "length")  # a value, not a file
        # A later job of the same bot job reads the file by handle...
        chained = self.request(inputs={"file": {"handle": first + "/echo"}}, settings={})
        done = self.run_job(jobs, second, chained)
        self.assertEqual(jobs.output_file(second, "echo").read_bytes(), b"hihihi")
        # ...but another bot job, a missing file or a path can't.
        for bad in ({"handle": first + "/echo"}, {"handle": first + "/nope"}, {"handle": "../" + first}):
            req = {**chained, "inputs": {"file": bad}}
            if bad["handle"] == first + "/echo":
                req["scope"] = "e"*64
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                jobs.poll("3"*64, req)
        # A fixed job can't change.
        with self.assertRaises(ValueError):
            jobs.poll(first, self.request(settings={"repeat": 4}))

    def test_failures_report_a_code(self):
        root, jobs = self.jobs()
        failed = self.run_job(jobs, "4"*64, self.request(inputs={"text": {"type": "text", "text": "fail"}}))
        self.assertEqual(failed, {"stage": "failed", "protocol": 4, "code": "too_long"})
        crashed = self.run_job(jobs, "5"*64, self.request(inputs={"text": {"type": "text", "text": "crash"}}))
        self.assertEqual(crashed, {"stage": "failed", "protocol": 4, "code": "task_failed"})
        for bad in (self.request(task="test.nope"), self.request(version=1), self.request(scope="x"),
                    self.request(deadline=int(time.time())+2*worker.MAX_TASK_SECONDS), self.request(settings={"x": 1}),
                    self.request(extra=1)):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                jobs.poll("6"*64, bad)

    def test_a_stopped_download_reports_why(self):
        root, jobs = self.jobs()
        request = {"protocol": 4, "task": "video.download", "version": 1, "scope": self.scope,
                   "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}}, "deadline": int(time.time())+600}
        for n, (error, code) in enumerate(((RuntimeError("progress_stalled"), "progress_stalled"),
                                           (RuntimeError("process_failed"), "process_failed"),
                                           (RuntimeError("secret detail from a subprocess"), "task_failed"))):
            with self.subTest(code=code), mock.patch.object(worker, "run", side_effect=error):
                failed = self.run_job(jobs, str(n+1)*64, request)
            self.assertEqual(failed["code"], code)

    def test_builtin_video_tasks(self):
        root, jobs = self.jobs()
        download, compress = "7"*64, "8"*64
        def run(args, deadline, **kwargs):
            out = Path(args[args.index("--output")+1]); receipt = Path(args[args.index("--receipt")+1])
            (out / "video.mp4").write_bytes(b"synthetic-original")
            receipt.write_text(json.dumps({"files": [str(out / "video.mp4")]}))
        request = {"protocol": 4, "task": "video.download", "version": 1, "scope": self.scope,
                   "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}}, "deadline": int(time.time())+600}
        with mock.patch.object(worker, "run", side_effect=run):
            done = self.run_job(jobs, download, request)
        self.assertEqual(done["outputs"]["original"]["handle"], download + "/original")
        seen = {}
        def compress_video(source, destination, limit, deadline, settings):
            seen.update(source=source.read_bytes(), limit=limit, settings=settings)
            destination.write_bytes(b"synthetic-send-copy"); return destination
        request = {"protocol": 4, "task": "video.compress", "version": 1, "scope": self.scope,
                   "inputs": {"source": {"handle": download + "/original"}},
                   "settings": {"preset": "fast", "passes": 1, "max_bytes": 500000}, "deadline": int(time.time())+600}
        with mock.patch.object(worker, "compress", side_effect=compress_video):
            done = self.run_job(jobs, compress, request)
        self.assertEqual(seen["source"], b"synthetic-original")
        self.assertEqual((seen["limit"], seen["settings"]["preset"], seen["settings"]["max_mb"]), (500000, "fast", .5))
        self.assertEqual(jobs.output_file(compress, "video").read_bytes(), b"synthetic-send-copy")
        for bad in ({"preset": "fast"}, {"max_bytes": 20000000}, {"max_bytes": 500000, "encoder": "x"}):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                jobs.poll("9"*64, {**request, "settings": bad})

    def test_http_v4(self):
        root, _ = self.jobs()
        token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
        server = worker.create_server(root, token, 0, tasks=worker.load_tasks(root))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        base = "http://127.0.0.1:%s/download" % server.server_port
        def call(method, path, value=None):
            req = urllib.request.Request(base + path, method=method, data=None if value is None else json.dumps(value).encode(),
                                         headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
            try:
                with opener.open(req, timeout=5) as response:
                    return response.status, response.read()
            except urllib.error.HTTPError as error:
                return error.code, b""
        try:
            status, body = call("GET", "/capabilities")
            self.assertEqual(json.loads(body)["tasks"], {"test.echo": 2, "video.compress": 1, "video.download": 1})
            job = "a"*64
            with mock.patch.object(worker.Jobs, "execute"):
                status, body = call("POST", "/jobs/" + job, self.request())
            self.assertEqual((status, json.loads(body)), (200, {"stage": "queued", "protocol": 4}))
            self.assertEqual(call("GET", "/jobs/" + job + "/files/echo")[0], 502)  # not done yet
            self.assertEqual(call("POST", "/jobs/" + job, self.request(task="test.nope"))[0], 502)
        finally:
            server.shutdown(); server.server_close(); thread.join(timeout=3)
