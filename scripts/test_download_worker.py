import errno
import importlib.util
import io
import json
import os
import socket
import subprocess
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

    def test_second_instance_fails_without_touching_scratch(self):
        worker_script = Path(__file__).with_name("download-worker.py")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "research" / "rednote_downloader" / "app").mkdir(parents=True)
            (root / "research" / "rednote_downloader" / "app" / "downloader.py").write_text("# stub\n", encoding="utf-8")
            out = root / "out"
            token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
            token_file = root / "token"
            token_file.write_text(token, encoding="ascii")

            first = worker.create_server(root, token, 0, output_root=out, bind="127.0.0.1")
            thread = threading.Thread(target=first.serve_forever, daemon=True)
            thread.start()
            try:
                sentinel = out / ".scratch" / "job-live" / "working.mp4"
                sentinel.parent.mkdir(parents=True)
                sentinel.write_bytes(b"in-progress-scratch-data")
                self.assertEqual((out / "worker.pid").read_text(encoding="ascii").strip(), str(os.getpid()))

                with self.assertRaises(OSError):
                    worker.create_server(root, token, first.server_port, output_root=out, bind="127.0.0.1")
                self.assertEqual(sentinel.read_bytes(), b"in-progress-scratch-data")
                self.assertEqual((out / "worker.pid").read_text(encoding="ascii").strip(), str(os.getpid()))

                # Also verify across a second OS process (exercises SO_EXCLUSIVEADDRUSE on Windows).
                second_proc = subprocess.run(
                    [sys.executable, str(worker_script), "--root", str(root), "--output-root", str(out),
                     "--token-file", str(token_file), "--bind", "127.0.0.1", "--port", str(first.server_port)],
                    capture_output=True, text=True, timeout=15,
                )
                self.assertNotEqual(second_proc.returncode, 0)
                self.assertNotEqual(second_proc.returncode, worker.EXIT_ADDR_NOT_AVAILABLE)
                self.assertEqual(sentinel.read_bytes(), b"in-progress-scratch-data")
                self.assertEqual((out / "worker.pid").read_text(encoding="ascii").strip(), str(os.getpid()))

                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                req = urllib.request.Request(
                    f"http://127.0.0.1:{first.server_port}/download/capabilities",
                    headers={"Authorization": "Bearer " + token},
                )
                with opener.open(req, timeout=3) as resp:
                    self.assertEqual(resp.status, 200)
            finally:
                first.shutdown()
                first.server_close()
                thread.join(timeout=3)
            self.assertFalse((out / "worker.pid").exists())

    def test_address_unavailable_exits_with_distinct_code(self):
        worker_script = Path(__file__).with_name("download-worker.py")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "research" / "rednote_downloader" / "app").mkdir(parents=True)
            (root / "research" / "rednote_downloader" / "app" / "downloader.py").write_text("# stub\n", encoding="utf-8")
            token_file = root / "token"
            token_file.write_text("SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY", encoding="ascii")
            out = root / "out"
            argv = ["--root", str(root), "--output-root", str(out), "--token-file", str(token_file),
                    "--bind", "100.127.255.254", "--port", "0"]

            posix_err = OSError(errno.EADDRNOTAVAIL, "Cannot assign requested address")
            win_err = OSError("The requested address is not valid in its context")
            win_err.winerror = 10049
            for err in (posix_err, win_err):
                with self.subTest(err=err), mock.patch.object(worker, "create_server", side_effect=err), \
                        mock.patch("sys.stderr", io.StringIO()):
                    with self.assertRaises(SystemExit) as ctx:
                        worker.main(argv)
                    self.assertEqual(ctx.exception.code, worker.EXIT_ADDR_NOT_AVAILABLE)

            unassigned = None
            for candidate in ("100.127.255.254", "100.127.255.253", "100.64.255.254"):
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe_sock:
                    try:
                        probe_sock.bind((candidate, 0))
                    except OSError as exc:
                        if worker.is_addr_not_available(exc):
                            unassigned = candidate
                            break
            if unassigned is not None:
                res = subprocess.run(
                    [sys.executable, str(worker_script), "--root", str(root), "--output-root", str(out),
                     "--token-file", str(token_file), "--bind", unassigned, "--port", "0"],
                    capture_output=True, text=True, timeout=15,
                )
                self.assertEqual(res.returncode, worker.EXIT_ADDR_NOT_AVAILABLE)
                self.assertFalse((out / ".scratch").exists())

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

    def test_lease_constant_used_on_start_and_renew(self):
        self.assertGreaterEqual(worker.LEASE_SECONDS, 300)
        _, jobs = self.jobs()
        job = "a"*64
        req = self.request()
        with mock.patch.object(worker.threading, "Thread"), \
                mock.patch.object(worker.time, "monotonic", side_effect=[1000.0, 1050.0]):
            self.assertEqual(jobs.poll(job, req), {"stage": "queued", "protocol": 4})
            self.assertEqual(jobs.active[job], 1000.0 + worker.LEASE_SECONDS)
            self.assertEqual(jobs.poll(job, req), {"stage": "queued", "protocol": 4})
            self.assertEqual(jobs.active[job], 1050.0 + worker.LEASE_SECONDS)

    def test_terminal_jobs_not_overwritten_to_expired_past_deadline(self):
        _, jobs = self.jobs()
        past_deadline = int(time.time()) - 2
        for stage, extra in (("complete", {"outputs": {"echo": {"type": "file", "handle": "1"*64 + "/echo", "size": 2}}}),
                             ("failed", {"code": "video_too_long"}),
                             ("cancelled", {})):
            job = {"complete": "1", "failed": "2", "cancelled": "4"}[stage] * 64
            req = self.request(deadline=past_deadline)
            jobs.save(jobs.path(job), {**req, "stage": stage, **extra})
            res = jobs.poll(job, req)
            self.assertEqual(res["stage"], stage)
            self.assertEqual(jobs.load(jobs.path(job))["stage"], stage)
        queued_job = "3" * 64
        req = self.request(deadline=past_deadline)
        jobs.save(jobs.path(queued_job), {**req, "stage": "queued"})
        res = jobs.poll(queued_job, req)
        self.assertEqual(res, {"stage": "expired", "protocol": 4})
        self.assertEqual(jobs.load(jobs.path(queued_job))["stage"], "expired")

    def test_cancel_scope_stops_active_job_frees_slot_and_blocks_later_polls(self):
        root, _ = self.jobs()
        entered = threading.Event()
        runs = []

        def run_slow(job):
            runs.append(job.id)
            job.stage("downloading")
            entered.set()
            job.run([sys.executable, "-c", "import time; time.sleep(30)"])

        tasks = worker.load_tasks(root)
        tasks["test.slow"] = {"version": 1, "validate": lambda i, s: (i, {}), "run": run_slow}
        token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
        server = worker.create_server(root, token, 0, tasks=tasks, slots=1)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        base = "http://127.0.0.1:%s/download" % server.server_port

        def call(method, path, value=None, auth="Bearer " + token):
            headers = {"Authorization": auth}
            data = b"" if method == "POST" and value is None else None
            if value is not None:
                data = json.dumps(value).encode()
                headers["Content-Type"] = "application/json"
            req = urllib.request.Request(base + path, method=method, data=data, headers=headers)
            try:
                with opener.open(req, timeout=5) as response:
                    return response.status, json.loads(response.read())
            except urllib.error.HTTPError as error:
                return error.code, None

        try:
            scope_a, scope_b, unknown_scope = "a" * 64, "b" * 64, "0" * 64
            slow_job, fast_job, later_job, disk_job = "1" * 64, "2" * 64, "3" * 64, "4" * 64
            deadline = int(time.time()) + 600
            slow_req = {"protocol": 4, "task": "test.slow", "version": 1, "scope": scope_a,
                        "inputs": {}, "settings": {}, "deadline": deadline}
            fast_req = self.request(scope=scope_b, deadline=deadline)

            # Auth required, bad paths rejected, unknown scope is a 200 no-op with empty body.
            self.assertEqual(call("POST", "/scopes/" + scope_a + "/cancel", auth="Bearer wrong")[0], 401)
            self.assertEqual(call("GET", "/scopes/" + scope_a + "/cancel")[0], 404)
            self.assertEqual(call("POST", "/scopes/bad/cancel")[0], 502)
            self.assertEqual(call("POST", "/scopes/" + unknown_scope + "/cancel"),
                             (200, {"protocol": 4, "cancelled": 0}))

            # Start slow_job on the single slot, then queue fast_job behind it.
            self.assertEqual(call("POST", "/jobs/" + slow_job, slow_req)[0], 200)
            self.assertTrue(entered.wait(timeout=5), "slow job never entered run()")
            self.assertEqual(call("POST", "/jobs/" + fast_job, fast_req),
                             (200, {"stage": "queued", "protocol": 4}))

            # Cancel scope_a: kills slow_job within ~1s, frees the slot for fast_job.
            started = time.monotonic()
            self.assertEqual(call("POST", "/scopes/" + scope_a + "/cancel"),
                             (200, {"protocol": 4, "cancelled": 1}))
            fast_done = None
            while time.monotonic() - started < 10.0:
                _, body = call("POST", "/jobs/" + fast_job, fast_req)
                if body and body.get("stage") == "complete":
                    fast_done = body
                    break
                time.sleep(0.05)
            self.assertIsNotNone(fast_done, "cancelling active job did not free slot quickly")
            self.assertLess(time.monotonic() - started, 10.0)

            # Cancelled job stays cancelled on disk (not overwritten to queued on LeaseExpired)
            # and a later poll neither restarts slow_job nor starts new/queued jobs in scope_a.
            time.sleep(0.1)
            saved = json.loads((root / "downloads" / "smsbot" / slow_job / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(saved["stage"], "cancelled")
            self.assertEqual(call("POST", "/jobs/" + slow_job, slow_req),
                             (200, {"stage": "cancelled", "protocol": 4}))
            self.assertEqual(runs, [slow_job])

            self.assertEqual(call("POST", "/jobs/" + later_job, slow_req),
                             (200, {"stage": "cancelled", "protocol": 4}))
            later_saved = json.loads((root / "downloads" / "smsbot" / later_job / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(later_saved["stage"], "cancelled")

            # A pre-existing non-terminal job on disk for the cancelled scope becomes cancelled on poll.
            disk_dir = root / "downloads" / "smsbot" / disk_job
            disk_dir.mkdir(parents=True, exist_ok=True)
            (disk_dir / "state.json").write_text(json.dumps({**slow_req, "stage": "queued"}), encoding="utf-8")
            self.assertEqual(call("POST", "/jobs/" + disk_job, slow_req),
                             (200, {"stage": "cancelled", "protocol": 4}))
            self.assertEqual(json.loads((disk_dir / "state.json").read_text(encoding="utf-8"))["stage"], "cancelled")
            self.assertEqual(runs, [slow_job])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=3)

    def test_busy_returns_503(self):
        root, _ = self.jobs()
        token = "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY"
        server = worker.create_server(root, token, 0, tasks=worker.load_tasks(root))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        base = "http://127.0.0.1:%s/download" % server.server_port
        try:
            with mock.patch.object(worker.Jobs, "poll", side_effect=RuntimeError("busy")):
                req = urllib.request.Request(
                    base + "/jobs/" + "a"*64,
                    method="POST",
                    data=json.dumps(self.request()).encode(),
                    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
                )
                with self.assertRaises(urllib.error.HTTPError) as ctx:
                    opener.open(req, timeout=5)
                self.assertEqual(ctx.exception.code, 503)
        finally:
            server.shutdown(); server.server_close(); thread.join(timeout=3)

    def test_compress_task_failed_code_survives(self):
        root, jobs = self.jobs()
        download, compress_job = "a"*64, "b"*64
        path = jobs.path(download)
        (path / "files").mkdir()
        (path / "files" / "original").write_bytes(b"synthetic-video")
        jobs.save(path, {
            "protocol": 4, "task": "video.download", "version": 1, "scope": self.scope,
            "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}},
            "settings": {}, "deadline": int(time.time()) + 600, "stage": "complete",
            "outputs": {"original": {"type": "video", "handle": download + "/original", "size": 15}},
        })
        req = {
            "protocol": 4, "task": "video.compress", "version": 1, "scope": self.scope,
            "inputs": {"source": {"handle": download + "/original"}},
            "settings": {"max_bytes": 200000, "copy_if_fits": False}, "deadline": int(time.time()) + 600,
        }
        fake_probe = {"format": {"duration": "36000"}, "streams": [{"codec_type": "video"}]}
        with mock.patch.object(worker, "probe", return_value=fake_probe):
            failed = self.run_job(jobs, compress_job, req)
        self.assertEqual(failed, {"stage": "failed", "protocol": 4, "code": "video_too_long"})

    def test_download_source_receipt_code_becomes_job_code(self):
        _, jobs = self.jobs()
        request = {
            "protocol": 4, "task": "video.download", "version": 1, "scope": self.scope,
            "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}},
            "deadline": int(time.time()) + 600,
        }
        for n, (payload, expected_code) in enumerate((
            ({"code": "single_video_required"}, "single_video_required"),
            ({"code": "unsupported_platform"}, "unsupported_platform"),
            ({"code": "bad code https://example.com"}, "process_failed"),
            (None, "process_failed"),
        )):
            def fake_run(args, deadline, **kwargs):
                if payload is not None:
                    receipt = Path(args[args.index("--receipt") + 1])
                    receipt.write_text(json.dumps(payload), encoding="utf-8")
                raise RuntimeError("process_failed")
            with self.subTest(expected_code=expected_code), mock.patch.object(worker, "run", side_effect=fake_run):
                failed = self.run_job(jobs, str(n + 1) * 64, request)
            self.assertEqual(failed, {"stage": "failed", "protocol": 4, "code": expected_code})

    def test_download_source_cli_writes_receipt_only_for_allowed_codes(self):
        source_script = Path(__file__).with_name("download-source.py")
        import subprocess
        for exc_msg, expect_receipt in (
            ("single_video_required", {"code": "single_video_required"}),
            ("unsupported_platform", {"code": "unsupported_platform"}),
            ("https://secret.example/do-not-leak", None),
        ):
            with self.subTest(exc_msg=exc_msg), tempfile.TemporaryDirectory() as tmp:
                tmp_path = Path(tmp)
                receipt = tmp_path / "receipt.json"
                output = tmp_path / "originals"
                output.mkdir()
                runner = tmp_path / "run_cli.py"
                runner.write_text(
                    "import runpy, sys\n"
                    "from unittest import mock\n"
                    f"sys.argv = [{str(source_script)!r}, '--root', {str(tmp_path)!r}, '--output', {str(output)!r}, '--receipt', {str(receipt)!r}, 'https://v.douyin.com/synthetic/']\n"
                    "with mock.patch.dict('sys.modules', {}):\n"
                    "    import types\n"
                    "    app = types.ModuleType('app')\n"
                    "    dl = types.ModuleType('app.downloader'); dl.DownloaderConfig = object; dl.MediaDownloader = object\n"
                    "    pl = types.ModuleType('app.platforms'); pl.identify_url = lambda u: (_ for _ in ()).throw(ValueError(" + repr(exc_msg) + "))\n"
                    "    md = types.ModuleType('app.models'); md.Platform = object\n"
                    "    pw = types.ModuleType('playwright'); pws = types.ModuleType('playwright.sync_api'); pws.BrowserType = object\n"
                    "    sc = types.ModuleType('scripts'); dls = types.ModuleType('scripts.douyin_item_live_smoke'); dls.anonymous_browser_adapter = object\n"
                    "    sys.modules.update({'app': app, 'app.downloader': dl, 'app.platforms': pl, 'app.models': md, 'playwright': pw, 'playwright.sync_api': pws, 'scripts': sc, 'scripts.douyin_item_live_smoke': dls})\n"
                    f"    runpy.run_path({str(source_script)!r}, run_name='__main__')\n",
                    encoding="utf-8",
                )
                res = subprocess.run([sys.executable, str(runner)], capture_output=True, text=True)
                self.assertNotEqual(res.returncode, 0)
                if expect_receipt is None:
                    self.assertFalse(receipt.exists())
                else:
                    self.assertEqual(json.loads(receipt.read_text(encoding="utf-8")), expect_receipt)


class CleanupTests(unittest.TestCase):
    def make_job(self, jobs, job_id, stage, mtime, deadline, now):
        path = jobs.path(job_id)
        (path / "files").mkdir(exist_ok=True)
        (path / "files" / "original").write_bytes(b"synthetic-video")
        state = {
            "protocol": 4,
            "task": "video.download",
            "version": 1,
            "scope": "f" * 64,
            "inputs": {"url": {"type": "url", "text": "https://v.douyin.com/synthetic/"}},
            "settings": {},
            "deadline": deadline,
            "stage": stage,
        }
        jobs.save(path, state)
        os.utime(path / "state.json", (mtime, mtime))
        return path

    def test_cleanup_disabled_by_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root)
            now = 2_000_000_000.0
            old = self.make_job(jobs, "1" * 64, "complete", now - 100 * 86400, now - 100 * 86400, now)
            self.assertEqual(jobs.cleanup_old_jobs(None, now=now), [])
            self.assertTrue(old.exists())

    def test_cleanup_old_terminal_and_past_deadline_jobs_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root)
            now = 2_000_000_000.0
            day = 86400.0

            # Eligible: terminal + old mtime
            old_complete = self.make_job(jobs, "1" * 64, "complete", now - 8 * day, now + 600, now)
            old_failed = self.make_job(jobs, "2" * 64, "failed", now - 8 * day, now + 600, now)
            old_expired = self.make_job(jobs, "3" * 64, "expired", now - 8 * day, now - 8 * day, now)
            old_cancelled = self.make_job(jobs, "b" * 64, "cancelled", now - 8 * day, now + 600, now)
            # Eligible: terminal + recent mtime, but deadline passed > N days ago
            old_deadline = self.make_job(jobs, "4" * 64, "complete", now - 1 * day, now - 8 * day, now)
            # Eligible: past-deadline orphaned non-terminal jobs older than N days
            old_orphan_queued = self.make_job(jobs, "c" * 64, "queued", now - 8 * day, now - 8 * day, now)
            old_orphan_dl = self.make_job(jobs, "7" * 64, "downloading", now - 10 * day, now - 10 * day, now)

            # Preserved: terminal + within N days
            fresh_complete = self.make_job(jobs, "5" * 64, "complete", now - 6 * day, now - 6 * day, now)
            fresh_cancelled = self.make_job(jobs, "d" * 64, "cancelled", now - 2 * day, now - 2 * day, now)
            # Preserved: past-deadline orphan within N days (subject to --keep-days age rule)
            fresh_orphan = self.make_job(jobs, "e" * 64, "queued", now - 2 * day, now - 2 * day, now)
            # Preserved: non-terminal whose deadline has not passed, even with old mtime
            non_terminal_queued = self.make_job(jobs, "6" * 64, "queued", now - 10 * day, now + 600, now)
            non_terminal_dl = self.make_job(jobs, "f" * 64, "downloading", now - 10 * day, now + 600, now)
            # Preserved: active job even if terminal/past-deadline/old on disk
            active_job = self.make_job(jobs, "8" * 64, "queued", now - 10 * day, now - 10 * day, now)
            jobs.active[active_job.name] = time.monotonic() + 300

            # Preserved: non-job dir, missing state.json, corrupt state.json, and .scratch
            other_dir = jobs.directory / "not-a-job"
            other_dir.mkdir()
            no_state = jobs.path("9" * 64)
            corrupt = jobs.path("a" * 64)
            (corrupt / "state.json").write_text("{bad json", encoding="utf-8")
            os.utime(corrupt / "state.json", (now - 10 * day, now - 10 * day))

            deleted = jobs.cleanup_old_jobs(7, now=now)
            expected_deleted = {
                old_complete, old_failed, old_expired, old_cancelled,
                old_deadline, old_orphan_queued, old_orphan_dl,
            }
            self.assertEqual(set(deleted), {p.name for p in expected_deleted})
            for gone in expected_deleted:
                self.assertFalse(gone.exists())
            for kept in (fresh_complete, fresh_cancelled, fresh_orphan, non_terminal_queued, non_terminal_dl, active_job, other_dir, no_state, corrupt, jobs.scratch):
                self.assertTrue(kept.exists(), f"{kept} should have been kept")

    def test_keep_days_validation_and_server_startup_cleanup(self):
        self.assertEqual(worker.positive_days("7"), 7.0)
        self.assertEqual(worker.positive_days("0.5"), 0.5)
        for bad in ("0", "-3", "nan", "inf", "abc"):
            with self.subTest(bad=bad), self.assertRaises(Exception):
                worker.positive_days(bad)

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            jobs = worker.Jobs(root)
            old_ts = time.time() - 10 * 86400
            old = self.make_job(jobs, "b" * 64, "complete", old_ts, old_ts, time.time())
            fresh = self.make_job(jobs, "c" * 64, "complete", time.time(), time.time() + 600, time.time())
            server = worker.create_server(root, "SYNTHETIC_TOKEN_FOR_OFFLINE_TEST_ONLY", 0, keep_days=7)
            try:
                self.assertFalse(old.exists())
                self.assertTrue(fresh.exists())
            finally:
                server.server_close()
