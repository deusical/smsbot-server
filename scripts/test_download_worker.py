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
                self.assertEqual(json.loads(next(root.rglob("state.json")).read_text())["url"], valid["url"])
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=3)

    def test_checkpoint_restart_reuses_original_and_result(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root)
            job = "b"*64
            path = jobs.path(job)
            request = {"url": "https://xhslink.com/synthetic", "max_bytes": 100000, "deadline": int(time.time())+900}
            jobs.save(path, {**request, "stage": "compressing"})
            (path / "original.mp4").write_bytes(b"synthetic-original")
            # A fresh process has no in-memory state. The saved original must
            # suffice; source download is forbidden in this test.
            restarted = worker.Jobs(root)
            restarted.active[job] = time.monotonic()+30
            def compress(source, destination, limit, deadline):
                self.assertEqual(source.read_bytes(), b"synthetic-original")
                destination.write_bytes(b"synthetic-send-copy")
                return destination
            with mock.patch.object(worker, "compress", side_effect=compress), mock.patch.object(worker, "run", side_effect=AssertionError("redownloaded")):
                restarted.execute(job)
            self.assertEqual(restarted.poll(job, request), {"stage": "complete"})
            third = worker.Jobs(root)
            self.assertEqual(third.result(job).read_bytes(), b"synthetic-send-copy")
            self.assertEqual((path / "original.mp4").read_bytes(), b"synthetic-original")
            # A result over the job's byte limit is never served.
            (path / "result.mp4").write_bytes(b"x" * (request["max_bytes"] + 1))
            with self.assertRaises(ValueError):
                third.result(job)

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
                    self.assertEqual(json.loads(response.read()), {"protocol": 2, "protocols": [1, 2, 3, 4], "tasks": {"video.compress": 1, "video.download": 1}})
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
            root = Path(tmp); jobs = worker.Jobs(root); job = "c"*64
            settings = {**worker.DEFAULT_SETTINGS, "preset": "fast", "passes": 1, "max_attempts": 2,
                        "copy_if_fits": False, "max_mb": 1, "execution_minutes": 5}
            request = {"url": "https://xhslink.com/synthetic", "max_bytes": 500000,
                       "deadline": int(time.time())+300, "protocol": 2, "settings": settings}
            with mock.patch.object(worker.threading, "Thread"):
                jobs.poll(job, request)
            path = jobs.path(job)
            effective = {**settings, "max_mb": .5}
            self.assertEqual(jobs.load(path)["settings"], effective)
            (path / "original.mp4").write_bytes(b"synthetic-original")
            restarted = worker.Jobs(root)
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
            for encoder in ("libx264", "h264_nvenc"):
                for passes in (1, 2):
                    calls = self.encode({"preset": preset, "passes": passes}, encoder)
                    encodes = [args for args in calls if "-c:v" in args]
                    self.assertEqual(len(encodes), passes if encoder == "libx264" else 1)
                    for args in encodes:
                        self.assertEqual(args[args.index("-preset")+1], preset if encoder == "libx264" else nvenc)
                        self.assertEqual(args[args.index("-vf")+1], worker.SCALE)
                        if encoder == "libx264":
                            self.assertEqual(args[args.index("-threads")+1], str(os.cpu_count() or 2))
                            self.assertEqual("-pass" in args, passes == 2)
                        else:
                            self.assertEqual(args[args.index("-multipass")+1], "fullres" if passes == 2 else "disabled")
                    self.assertEqual(sum("-xerror" in args for args in calls), 1)

    def test_copy_attempts_and_max_mb(self):
        self.assertFalse(any("-c:v" in args for args in self.encode({}, source_size=10)))
        self.assertTrue(any("-c:v" in args for args in self.encode({"copy_if_fits": False}, source_size=10)))
        for encoder in ("libx264", "h264_nvenc"):
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


class ProtocolV3Tests(unittest.TestCase):
    scope = "d"*64
    download = "1"*64
    compress_job = "2"*64

    def jobs(self):
        folder = tempfile.TemporaryDirectory(); self.addCleanup(folder.cleanup)
        root = Path(folder.name)
        return root, worker.Jobs(root)

    def download_request(self, **extra):
        return {"protocol": 3, "op": "download", "scope": self.scope,
                "url": "https://v.douyin.com/synthetic/", "deadline": int(time.time())+600, **extra}

    def compress_request(self, **extra):
        return {"protocol": 3, "op": "compress", "scope": self.scope, "source": self.download,
                "max_bytes": 500000, "deadline": int(time.time())+600,
                "settings": {"preset": "fast", "passes": 1, "execution_minutes": 15}, **extra}

    def finish_download(self, root, jobs):
        with mock.patch.object(worker.threading, "Thread"):
            self.assertEqual(jobs.poll(self.download, self.download_request()), {"stage": "queued", "protocol": 3})
        path = jobs.path(self.download)
        jobs.active[self.download] = time.monotonic()+30
        def run(args, deadline, **kwargs):
            out = Path(args[args.index("--output")+1]); receipt = Path(args[args.index("--receipt")+1])
            (out / "video.mp4").write_bytes(b"synthetic-original")
            receipt.write_text(json.dumps({"files": [str(out / "video.mp4")]}))
        with mock.patch.object(worker, "run", side_effect=run), \
             mock.patch.object(worker, "compress", side_effect=AssertionError("download job compressed")):
            jobs.execute(self.download)
        return path

    def test_parsing(self):
        self.assertIsNone(worker.protocol_settings(self.download_request()))
        self.assertEqual(worker.protocol_settings(self.compress_request())["preset"], "fast")
        for bad in (self.download_request(op="encode"), self.download_request(scope="../x"),
                    self.download_request(max_bytes=100000), self.download_request(settings={}),
                    self.compress_request(source="/etc/passwd"), self.compress_request(url="https://douyin.com/x"),
                    self.compress_request(settings={"encoder": "x"})):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                worker.protocol_settings(bad)

    def test_download_keeps_only_the_original(self):
        root, jobs = self.jobs()
        path = self.finish_download(root, jobs)
        state = jobs.load(path)
        self.assertEqual(state["stage"], "complete")
        self.assertEqual((path / "original.mp4").read_bytes(), b"synthetic-original")
        self.assertFalse((path / "result.mp4").exists())
        with self.assertRaises(ValueError):
            jobs.result(self.download)

    def test_compress_reads_the_scoped_source_and_resumes_with_its_settings(self):
        root, jobs = self.jobs()
        # No source yet: rejected before any job is created.
        with self.assertRaises(ValueError):
            jobs.poll(self.compress_job, self.compress_request())
        self.finish_download(root, jobs)
        # Another bot job's scope can't use this original.
        with self.assertRaises(ValueError):
            jobs.poll(self.compress_job, self.compress_request(scope="e"*64))
        with mock.patch.object(worker.threading, "Thread"):
            self.assertEqual(jobs.poll(self.compress_job, self.compress_request())["stage"], "queued")
        restarted = worker.Jobs(root)
        restarted.active[self.compress_job] = time.monotonic()+30
        seen = {}
        def compress(source, destination, limit, deadline, settings):
            seen.update(source=source.read_bytes(), settings=settings)
            destination.write_bytes(b"synthetic-send-copy"); return destination
        with mock.patch.dict(worker.DEFAULT_SETTINGS, {"preset": "veryfast"}), \
             mock.patch.object(worker, "compress", side_effect=compress), \
             mock.patch.object(worker, "run", side_effect=AssertionError("redownloaded")):
            restarted.execute(self.compress_job)
        self.assertEqual(seen["source"], b"synthetic-original")
        self.assertEqual((seen["settings"]["preset"], seen["settings"]["passes"]), ("fast", 1))
        self.assertEqual(restarted.result(self.compress_job).read_bytes(), b"synthetic-send-copy")
        with self.assertRaises(ValueError):
            restarted.poll(self.compress_job, self.compress_request(settings={"preset": "slow", "execution_minutes": 15}))

    def test_http_v3_shapes(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(worker.Jobs, "execute"):
            token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
            server = worker.create_server(Path(tmp), token, 0)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            base = "http://127.0.0.1:%s/download/jobs/" % server.server_port
            def call(job, value):
                req = urllib.request.Request(base + job, data=json.dumps(value).encode(),
                    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                try:
                    with opener.open(req) as response:
                        return response.status, json.loads(response.read())
                except urllib.error.HTTPError as error:
                    return error.code, None
            try:
                self.assertEqual(call(self.download, self.download_request()), (200, {"stage": "queued", "protocol": 3}))
                self.assertEqual(call(self.download, self.download_request(url="http://127.0.0.1/x"))[0], 502)
                # The source download isn't complete: compress is refused.
                self.assertEqual(call(self.compress_job, self.compress_request())[0], 502)
                self.assertEqual(call(self.compress_job, self.compress_request(max_bytes=20000000))[0], 502)
            finally:
                server.shutdown(); server.server_close(); thread.join(timeout=3)


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
