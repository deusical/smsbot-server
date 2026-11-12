"""Task worker for SmsBot: authenticated HTTP on loopback or a Tailscale address.

Runs named, versioned tasks for the bot: the built-in video download and
compression, plus any task_*.py file next to this script (see TASKS below).
Jobs are durable: their state, inputs, settings and output files are plain files
in the owner's download directory, and a restart resumes them on the next poll.
Subprocess output and HTTP bodies are never logged. The worker never sends
anything; only the bot does.
"""
import argparse
import importlib.util
import ipaddress
import re
import tempfile
import hmac
import json
import math
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

LIMIT = 20_000_000 - 1
work = threading.local()
# How long a download may show no new bytes before it counts as stuck.
DOWNLOAD_STALL_SECONDS = 300


DEFAULT_SETTINGS = {"preset": "medium", "passes": 2, "max_attempts": 3,
                    "copy_if_fits": True, "max_mb": 19.99, "execution_minutes": 15}

# Encoded copies are at most 1920 pixels on their long side, keeping the aspect
# ratio and never upscaling. At a send-sized byte budget a 4K encode only spends
# bits on detail a phone screen can't show, and it takes about four times as long.
MAX_LONG_SIDE = 1920
SCALE = (f"scale=w=min({MAX_LONG_SIDE}\\,iw):h=min({MAX_LONG_SIDE}\\,ih)"
         ":force_original_aspect_ratio=decrease:force_divisible_by=2")


def validate_settings(value):
    if not isinstance(value, dict) or set(value) - set(DEFAULT_SETTINGS):
        raise ValueError("invalid_settings")
    result = {**DEFAULT_SETTINGS, **value}
    if (result["preset"] not in ("veryfast", "faster", "fast", "medium", "slow") or
        type(result["passes"]) is not int or not 1 <= result["passes"] <= 2 or
        type(result["max_attempts"]) is not int or not 1 <= result["max_attempts"] <= 3 or
        type(result["copy_if_fits"]) is not bool or
        type(result["max_mb"]) not in (int, float) or not math.isfinite(result["max_mb"]) or
        not .1 <= result["max_mb"] <= 19.99 or
        type(result["execution_minutes"]) is not int or not 1 <= result["execution_minutes"] <= 60):
        raise ValueError("invalid_settings")
    return result


JOB = re.compile(r"[a-f0-9]{64}")


class LeaseExpired(Exception):
    pass


def valid_url(value):
    if not isinstance(value, str) or len(value) > 4096:
        return False
    try:
        u = urlsplit(value)
        return (u.scheme == "https" and not u.username and not u.password and
                u.port is None and any(u.hostname == h or (u.hostname or "").endswith("." + h)
                for h in ("douyin.com", "xiaohongshu.com", "xhslink.com")))
    except ValueError:
        return False


def run(args, deadline, capture=False):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError()
    folder = getattr(work, "folder", None)
    if folder and Path(args[0]).name == "ffmpeg":
        args = args[:1] + ["-progress", str(folder / "progress"), "-stats_period", "1"] + args[1:]
    p = subprocess.Popen(args, stdin=subprocess.DEVNULL,
                         stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL,
                         creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
                         start_new_session=os.name != "nt")
    last, signature = time.monotonic(), None
    try:
        while True:
            if time.monotonic() >= deadline:
                raise TimeoutError()
            if getattr(work, "alive", None) and not work.alive():
                raise LeaseExpired()
            if folder:
                current = []
                for f in sorted(folder.rglob("*")):
                    try:
                        if f.is_file():
                            if f.name == "progress":
                                with f.open("rb") as progress:
                                    progress.seek(max(0, f.stat().st_size-4096))
                                    values = re.findall(rb"(?:frame|out_time_us)=([0-9]+)", progress.read())
                                current.append((str(f), tuple(values[-2:])))
                            else:
                                current.append((str(f), f.stat().st_size))
                    except FileNotFoundError:
                        pass
                if current != signature:
                    signature, last = current, time.monotonic()
                if time.monotonic() - last > getattr(work, "stall_seconds", 120):
                    raise RuntimeError("progress_stalled")
            try:
                output, _ = p.communicate(timeout=min(1, max(.01, deadline-time.monotonic())))
                if p.returncode:
                    raise RuntimeError("process_failed")
                return output
            except subprocess.TimeoutExpired:
                pass
    finally:
        if p.poll() is None:
            if os.name == "nt":
                subprocess.run(["taskkill", "/PID", str(p.pid), "/T", "/F"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            else:
                os.killpg(p.pid, signal.SIGKILL)
            p.wait()


def probe(source, deadline):
    return json.loads(run(["ffprobe", "-v", "error", "-show_streams", "-show_format",
                           "-of", "json", str(source)], deadline, capture=True))


def compress(source, destination, limit, deadline, encoder=None, settings=None):
    """Use CPU H.264 two-pass on Linux, or the existing AV1 NVENC on Windows.

    Target just below the ceiling and re-encode with measured size correction
    if muxing overshoots. Keep the aspect ratio (portrait stays portrait) and
    cap only the long side at MAX_LONG_SIDE. x264 gets a thread per core; the
    service's lower priority and CPU quota keep the rest of the host responsive.
    """
    configured = settings is not None
    settings = validate_settings(settings or {})
    if configured:
        limit = min(limit, int(round(settings["max_mb"] * 1_000_000)))
    encoder = encoder or ("libx264" if os.name != "nt" else "h264_nvenc")
    info = probe(source, deadline)
    duration = float(info["format"]["duration"])
    if not math.isfinite(duration) or duration <= 0:
        raise ValueError("invalid_duration")
    if not any(s.get("codec_type") == "video" for s in info["streams"]):
        raise ValueError("video_required")
    audio = any(s.get("codec_type") == "audio" for s in info["streams"])
    if settings["copy_if_fits"] and source.stat().st_size <= limit:
        shutil.copy2(source, destination)
    else:
        target = int(limit * .985)
        audio_rate = 128000 if audio else 0
        rate = int(target * 8 / duration - audio_rate - 12000)
        if rate < 32000:
            raise ValueError("video_too_long")
        for _ in range(settings["max_attempts"]):
            base = ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
                 "-i", str(source), "-map", "0:v:0", "-map", "0:a:0?",
                 "-map_metadata", "-1", "-map_chapters", "-1", "-vf", SCALE]
            if encoder == "libx264":
                stats = str(destination.with_suffix(".pass"))
                video = ["-c:v", "libx264", "-preset", settings["preset"], "-threads", str(os.cpu_count() or 2), "-pix_fmt", "yuv420p", "-b:v", str(rate), "-passlogfile", stats]
                try:
                    if settings["passes"] == 2:
                        run(base + video + ["-pass", "1", "-an", "-f", "null", "-"], deadline)
                        final = video + ["-pass", "2"]
                    else:
                        final = video[:-2]  # No pass log for single-pass encoding.
                    run(base + final + ["-c:a", "aac", "-b:a", str(audio_rate or 128000), "-movflags", "+faststart", str(destination)], deadline)
                finally:
                    for log in destination.parent.glob(destination.with_suffix(".pass").name + "*"):
                        log.unlink(missing_ok=True)
            else:
                run(base + ["-c:v", "h264_nvenc", "-preset", {"veryfast": "p3", "faster": "p4", "fast": "p5", "medium": "p6", "slow": "p7"}[settings["preset"]], "-tune", "hq",
                 "-multipass", "fullres" if settings["passes"] == 2 else "disabled", "-rc", "vbr", "-b:v", str(rate),
                 "-maxrate", str(rate * 2), "-bufsize", str(rate * 4),
                 "-rc-lookahead", "32", "-spatial-aq", "1", "-temporal-aq", "1",
                 "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", str(audio_rate or 128000),
                 "-movflags", "+faststart", str(destination)], deadline)
            size = destination.stat().st_size
            if 0 < size <= limit:
                break
            rate = max(32000, int(rate * target / size * .98))
        else:
            raise ValueError("size_limit")
    if not 0 < destination.stat().st_size <= limit:
        raise ValueError("size_limit")
    if getattr(work, "stage", None):
        work.stage("verifying")
    result = probe(destination, deadline)
    if abs(float(result["format"]["duration"]) - duration) > .25:
        raise ValueError("duration_changed")
    if audio and not any(s.get("codec_type") == "audio" for s in result["streams"]):
        raise ValueError("audio_missing")
    run(["ffmpeg", "-nostdin", "-v", "error", "-xerror", "-i", str(destination),
         "-f", "null", "-"], deadline)
    return destination


def fetch_original(root, url, folder, deadline):
    """Download the linked video into folder with the Rednote runner."""
    originals = folder / "originals"
    originals.mkdir()
    receipt = folder / "receipt.json"
    python = root / "research/.oss-venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    # Opening the page and finding the video writes nothing for a while; only
    # the file transfer itself shows growth, so allow a longer quiet spell.
    work.stall_seconds = DOWNLOAD_STALL_SECONDS
    try:
        run([str(python), str(Path(__file__).with_name("download-source.py")),
             "--root", str(root), url, "--output", str(originals),
             "--receipt", str(receipt), "--timeout", str(max(1, int(deadline-time.monotonic())))], deadline)
    finally:
        work.__dict__.pop("stall_seconds", None)
    files = [Path(f).resolve() for f in json.loads(receipt.read_text())["files"]]
    if len(files) != 1 or not files[0].is_relative_to(originals.resolve()) or files[0].suffix.lower() != ".mp4":
        raise ValueError("single_video_required")
    return files[0]


def publish(source, destination):
    """Move a finished file into its job folder in one step, so a crash never
    leaves a partial original or result behind under its final name."""
    with source.open("r+b") as f:  # Windows can only flush a writable handle.
        os.fsync(f.fileno())
    source.replace(destination)


CODE = re.compile(r"[a-z][a-z0-9_]{0,47}")
STAGE = re.compile(r"[a-z][a-z_]{0,31}")
TASK_NAME = re.compile(r"[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+")
FILE_NAME = re.compile(r"[a-z][a-z0-9_]{0,31}")
# A task may run for up to a day; the bot's polls keep its lease alive.
MAX_TASK_SECONDS = 24 * 3600


class TaskFailed(Exception):
    """A task's own failure, reported to the bot as a short code."""
    def __init__(self, code):
        super().__init__(code)
        self.code = code if isinstance(code, str) and CODE.fullmatch(code) else "task_failed"


# Why a subprocess was stopped, kept as the job's failure code so the dashboard
# can say more than "task failed". Nothing from the subprocess itself is kept.
STOP_CODES = {"progress_stalled", "process_failed"}


def failure_code(error):
    if isinstance(error, TaskFailed):
        return error.code
    if isinstance(error, RuntimeError) and error.args and error.args[0] in STOP_CODES:
        return error.args[0]
    return "task_failed"


class TaskJob:
    """What a task's run(job) works with.

    inputs and settings are what the bot sent, after the task's validate.
    folder is private scratch space, removed afterwards; path is the job's own
    folder, which survives restarts. deadline is a time.monotonic() value.
    """
    def __init__(self, jobs, job, path, state, folder, deadline, stage):
        self.id, self.path, self.folder, self.deadline = job, path, folder, deadline
        self.inputs, self.settings, self.scope = state["inputs"], state["settings"], state["scope"]
        self.outputs = {}
        self._jobs, self._stage = jobs, stage

    def stage(self, name):
        """Report progress, such as "downloading"; the bot shows it in Activity."""
        if not isinstance(name, str) or not STAGE.fullmatch(name):
            raise ValueError("invalid_stage")
        self._stage(name)

    def fail(self, code):
        """End the task as failed with a short code, such as "too_long"."""
        raise TaskFailed(code)

    def run(self, args, capture=False):
        """Run a subprocess, stopped at the deadline, on a lost lease or a stall."""
        return run(args, self.deadline, capture)

    def file(self, value):
        """The local path of an input file: a file output of an earlier job of
        the same bot job on this worker, given as {"handle": "<job>/<name>"}."""
        return self._jobs.handle_path(value.get("handle") if isinstance(value, dict) else None, self.scope)

    def saved(self, name):
        """A file output this job already published before a restart, if any."""
        f = self.path / "files" / name
        return f if FILE_NAME.fullmatch(name) and f.is_file() else None

    def output(self, name, value):
        """Set a JSON output value, such as {"type": "text", "text": "..."}."""
        if not FILE_NAME.fullmatch(name):
            raise ValueError("invalid_output")
        json.dumps(value)
        self.outputs[name] = value

    def output_file(self, name, source, type):
        """Publish a file as an output; later jobs and the bot refer to it by handle."""
        if not FILE_NAME.fullmatch(name) or not isinstance(type, str):
            raise ValueError("invalid_output")
        destination = self.path / "files" / name
        if Path(source).resolve() != destination.resolve():
            destination.parent.mkdir(exist_ok=True, mode=0o700)
            publish(Path(source), destination)
        self.outputs[name] = {"type": type, "handle": f"{self.id}/{name}", "size": destination.stat().st_size}


def validate_download(inputs, settings):
    url = inputs.get("url") if isinstance(inputs, dict) else None
    if set(inputs) != {"url"} or not isinstance(url, dict) or not valid_url(url.get("text")) or settings:
        raise ValueError("invalid_request")
    return {"url": {"type": str(url.get("type", "url")), "text": url["text"]}}, {}


def run_download(job):
    original = job.saved("original")
    if original is None:
        job.stage("downloading")
        original = fetch_original(job._jobs.root, job.inputs["url"]["text"], job.folder, job.deadline)
    job.output_file("original", original, "video")


def validate_compress(inputs, settings):
    source = inputs.get("source") if isinstance(inputs, dict) else None
    if set(inputs) != {"source"} or not isinstance(source, dict) or not isinstance(source.get("handle"), str):
        raise ValueError("invalid_request")
    if not isinstance(settings, dict):
        raise ValueError("invalid_settings")
    settings = dict(settings)
    max_bytes = settings.pop("max_bytes", None)
    if type(max_bytes) is not int or not 100000 <= max_bytes <= LIMIT:
        raise ValueError("invalid_settings")
    settings = validate_settings(settings)
    settings["max_mb"] = min(settings["max_mb"], max_bytes / 1_000_000)
    return {"source": {"handle": source["handle"]}}, {**settings, "max_bytes": max_bytes}


def run_compress(job):
    source = job.file(job.inputs["source"])
    job.stage("compressing")
    settings = {k: v for k, v in job.settings.items() if k != "max_bytes"}
    output = compress(source, job.folder / "send.mp4", job.settings["max_bytes"], job.deadline, settings=settings)
    job.output_file("video", output, "video")


# A task is {"version": int, "validate": f(inputs, settings) -> (inputs, settings),
# "run": f(job)}. validate rejects a bad request with ValueError before any job
# exists; run does the work through a TaskJob. A task_*.py file next to this
# script adds tasks with a module-level TASKS dict of the same shape.
TASKS = {
    "video.download": {"version": 1, "validate": validate_download, "run": run_download},
    "video.compress": {"version": 1, "validate": validate_compress, "run": run_compress},
}


def load_tasks(directory):
    """The built-in tasks plus those of every task_*.py file in directory."""
    tasks = dict(TASKS)
    for f in sorted(Path(directory).glob("task_*.py")):
        spec = importlib.util.spec_from_file_location(f.stem, f)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        for name, task in getattr(module, "TASKS", {}).items():
            if (not isinstance(name, str) or not TASK_NAME.fullmatch(name) or name in tasks or not isinstance(task, dict) or
                    type(task.get("version")) is not int or task["version"] < 1 or not callable(task.get("run")) or
                    not callable(task.get("validate", lambda i, s: None))):
                raise SystemExit(f"Invalid task {name!r} in {f.name}.")
            tasks[name] = {"version": task["version"], "validate": task.get("validate") or (lambda i, s: (i, s)), "run": task["run"]}
    return tasks


class Jobs:
    """Plain-file checkpoints; a few job slots, short authenticated HTTP polls.

    Polls renew a 30-second lease. Loss of bot consent stops work when the
    lease expires; checkpoints survive. Only the bot can authorize sending.
    """
    def __init__(self, root, output_root=None, tasks=None, slots=1):
        self.root = root
        self.tasks = dict(TASKS) if tasks is None else tasks
        self.directory = output_root or root / "downloads" / "smsbot"
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.scratch = self.directory / ".scratch"
        # Clean only this worker's resolved scratch directory after a crash.
        if self.scratch.resolve().parent != self.directory.resolve():
            raise ValueError("invalid_scratch_directory")
        if self.scratch.exists():
            shutil.rmtree(self.scratch)
        self.scratch.mkdir(mode=0o700)
        self.lock = threading.RLock()
        self.active = {}
        # Jobs run in up to `slots` at once; the rest wait their turn.
        self.busy = threading.Semaphore(slots)

    def path(self, job):
        if not re.fullmatch(r"[a-f0-9]{64}", job):
            raise ValueError("invalid_job")
        path = self.directory / job
        path.mkdir(exist_ok=True, mode=0o700)
        return path

    def save(self, path, value):
        temporary = path / "state.tmp"
        with temporary.open("w", encoding="utf-8") as out:
            json.dump(value, out)
            out.flush()
            os.fsync(out.fileno())
        temporary.replace(path / "state.json")

    def load(self, path):
        return json.loads((path / "state.json").read_text(encoding="utf-8"))

    def task_request(self, request):
        """Validate a protocol-4 request into the job's fixed request."""
        if not isinstance(request, dict) or set(request) - {"protocol", "task", "version", "scope", "inputs", "settings", "deadline"}:
            raise ValueError("invalid_request")
        task = self.tasks.get(request.get("task"))
        if task is None or request.get("version") != task["version"]:
            raise ValueError("unknown_task")
        scope, deadline = request.get("scope"), request.get("deadline")
        if not isinstance(scope, str) or not JOB.fullmatch(scope):
            raise ValueError("invalid_request")
        if type(deadline) is not int or not time.time()-5 < deadline <= time.time()+MAX_TASK_SECONDS:
            raise ValueError("invalid_deadline")
        inputs, settings = request.get("inputs", {}), request.get("settings", {})
        if not isinstance(inputs, dict) or not isinstance(settings, dict):
            raise ValueError("invalid_request")
        inputs, settings = task["validate"](inputs, settings)
        for value in inputs.values():
            if isinstance(value, dict) and "handle" in value:
                self.handle_path(value["handle"], scope)
        return {"protocol": 4, "task": request["task"], "version": task["version"], "scope": scope,
                "inputs": inputs, "settings": settings, "deadline": deadline}

    def handle_path(self, handle, scope):
        """The file behind "<job>/<name>": a file output of a completed job of
        the same bot job (scope) on this worker."""
        job, _, name = handle.partition("/") if isinstance(handle, str) else ("", "", "")
        if not JOB.fullmatch(job) or not FILE_NAME.fullmatch(name) or not (self.directory / job / "state.json").exists():
            raise ValueError("source_unavailable")
        state = self.load(self.directory / job)
        f = self.directory / job / "files" / name
        if (state.get("protocol") != 4 or state.get("scope") != scope or state["stage"] != "complete" or
                name not in state.get("outputs", {}) or not f.is_file()):
            raise ValueError("source_unavailable")
        return f

    def poll(self, job, request):
        if not isinstance(request, dict) or request.get("protocol") != 4:
            raise ValueError("unsupported_protocol")
        with self.lock:
            request = self.task_request(request)
            path = self.path(job)
            if (path / "state.json").exists():
                state = self.load(path)
                if any(state.get(k) != request.get(k) for k in set(state) | set(request) if k not in ("stage", "outputs", "code")):
                    raise ValueError("immutable_job")
            else:
                state = {**request, "stage": "queued"}
                self.save(path, state)
            if time.time() >= state["deadline"]:
                state["stage"] = "expired"
                self.save(path, state)
            elif state["stage"] not in ("complete", "failed", "expired"):
                if job in self.active:
                    self.active[job] = time.monotonic() + 30
                elif len(self.active) < 100:
                    self.active[job] = time.monotonic() + 30
                    threading.Thread(target=self.execute, args=(job,), daemon=True).start()
                else:
                    raise RuntimeError("busy")
            answer = {"stage": state["stage"], "protocol": 4}
            if state["stage"] == "complete":
                answer["outputs"] = state.get("outputs", {})
            if state["stage"] == "failed":
                answer["code"] = state.get("code", "task_failed")
            return answer

    def execute(self, job):
        state, path = None, None
        try:
            with self.busy:
                with self.lock:
                    path = self.path(job)
                    state = self.load(path)
                def alive():
                    with self.lock:
                        return time.monotonic() < self.active.get(job, 0) and time.time() < state["deadline"]
                if not alive():
                    return
                def stage(value):
                    with self.lock:
                        state["stage"] = value
                        self.save(path, state)
                # Working files are transient, owner-only; only finished files
                # are published to the job folder as durable checkpoints.
                with tempfile.TemporaryDirectory(prefix="job-", dir=self.scratch) as tmp:
                    folder = Path(tmp)
                    work.folder, work.alive, work.stage = folder, alive, stage
                    deadline = time.monotonic() + max(0, state["deadline"] - time.time())
                    task = self.tasks.get(state.get("task"))
                    if state.get("protocol") != 4 or task is None or task["version"] != state.get("version"):
                        raise TaskFailed("unknown_task")
                    tj = TaskJob(self, job, path, state, folder, deadline, stage)
                    task["run"](tj)
                    if not alive():
                        raise LeaseExpired()
                    with self.lock:
                        state["outputs"] = tj.outputs
                    stage("complete")
        except (TimeoutError, LeaseExpired):
            with self.lock:
                state["stage"] = "expired" if time.time() >= state["deadline"] else "queued"
                self.save(path, state)
        except Exception as e:
            if state is None:
                return
            with self.lock:
                state["stage"] = "failed"
                state["code"] = failure_code(e)
                self.save(path, state)
        finally:
            work.__dict__.clear()
            with self.lock:
                self.active.pop(job, None)

    def output_file(self, job, name):
        """A file output of a completed protocol-4 job, for the bot to fetch."""
        with self.lock:
            path = self.path(job)
            state = self.load(path)
            f = path / "files" / name
            if (state.get("protocol") != 4 or state["stage"] != "complete" or not FILE_NAME.fullmatch(name) or
                    "handle" not in state.get("outputs", {}).get(name, {}) or not f.is_file()):
                raise ValueError("result_unavailable")
            return f


TAILNET = ipaddress.ip_network("100.64.0.0/10")


def bind_address(value):
    """Loopback, or this machine's Tailscale address: never a LAN or every interface.

    Tailscale encrypts and authenticates the link to the SmsBot server; plain
    HTTP on a LAN would expose the bearer token to anything on the network.
    """
    try:
        address = ipaddress.ip_address(value)
    except ValueError:
        raise ValueError("bind address must be an IP address")
    if address.version != 4 or not (address.is_loopback or address in TAILNET):
        raise ValueError("bind address must be 127.0.0.1 or a Tailscale 100.x address")
    return str(address)


def create_server(root, token, port, output_root=None, bind="127.0.0.1", tasks=None, slots=1):
    jobs = Jobs(root, output_root, tasks, slots)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def job_request(self):
            self.connection.settimeout(10)
            if not hmac.compare_digest(self.headers.get("Authorization", "").encode(), ("Bearer " + token).encode()):
                self.send_error(401)
                return
            try:
                if self.command == "GET" and self.path == "/download/capabilities":
                    data = json.dumps({"protocol": 4, "protocols": [4],
                                       "tasks": {n: t["version"] for n, t in sorted(jobs.tasks.items())}}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(data)))
                    self.send_header("Cache-Control", "no-store")
                    self.end_headers()
                    self.wfile.write(data)
                    return
                parts = self.path.split("/")
                if len(parts) not in (4, 5, 6) or parts[:3] != ["", "download", "jobs"]:
                    raise ValueError()
                job = parts[3]
                if not JOB.fullmatch(job):
                    raise ValueError()
                if self.command == "POST" and len(parts) == 4:
                    length = int(self.headers.get("Content-Length", "0"))
                    if not 0 < length <= 1 << 20:
                        raise ValueError()
                    request = json.loads(self.rfile.read(length))
                    if not isinstance(request, dict) or request.get("protocol") != 4:
                        raise ValueError()
                    data = json.dumps(jobs.poll(job, request)).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(data)))
                    self.send_header("Cache-Control", "no-store")
                    self.end_headers()
                    self.wfile.write(data)
                elif self.command == "GET" and len(parts) == 6 and parts[4] == "files":
                    output = jobs.output_file(job, parts[5])
                    with output.open("rb") as src:
                        self.send_response(200)
                        self.send_header("Content-Type", "application/octet-stream")
                        self.send_header("Content-Length", str(os.fstat(src.fileno()).st_size))
                        self.send_header("Cache-Control", "no-store")
                        self.end_headers()
                        shutil.copyfileobj(src, self.wfile)
                else:
                    self.send_error(404)
            except (BrokenPipeError, ConnectionResetError):
                pass
            except Exception:
                self.send_error(502, "download_failed")

        def do_GET(self):
            self.job_request()

        def do_POST(self):
            if self.path.startswith("/download/jobs/"):
                return self.job_request()
            # Old synchronous helpers must be upgraded with the bot.
            self.send_error(410, "async_download_required")

    return ThreadingHTTPServer((bind_address(bind), port), Handler)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--output-root", type=Path)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--port", type=int, default=8767)
    parser.add_argument("--slots", type=int, default=1, choices=range(1, 9),
                        help="jobs run at once (default 1)")
    parser.add_argument("--bind", default="127.0.0.1",
                        help="127.0.0.1 (default), or this machine's Tailscale 100.x address "
                             "to serve a SmsBot running on another machine")
    args = parser.parse_args()
    try:
        bind_address(args.bind)
    except ValueError as e:
        raise SystemExit(str(e))
    if not (args.root / "research/rednote_downloader/app/downloader.py").is_file():
        raise SystemExit("Existing Rednote engine not found.")
    if not args.token_file.exists():
        args.token_file.parent.mkdir(parents=True, exist_ok=True)
        with args.token_file.open("x", encoding="ascii") as f:
            f.write(secrets.token_urlsafe(48))
        if os.name != "nt":
            args.token_file.chmod(0o600)
    worker_token = args.token_file.read_text(encoding="ascii").strip()
    if len(worker_token) < 32:
        raise SystemExit("Invalid worker token file.")
    print(f"Download worker ready on {args.bind}:{args.port}; originals remain in the configured downloads folder.", flush=True)
    tasks = load_tasks(Path(__file__).parent)
    print("Tasks: " + ", ".join(f"{n}@{t['version']}" for n, t in sorted(tasks.items())), flush=True)
    create_server(args.root.resolve(), worker_token, args.port, output_root=args.output_root,
                  bind=args.bind, tasks=tasks, slots=args.slots).serve_forever()
