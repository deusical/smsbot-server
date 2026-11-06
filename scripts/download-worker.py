"""Server-local video worker for SmsBot. Only authenticated loopback HTTP.

Uses the existing downloader's anonymous Rednote runner, never a browser vault.
Subprocess output and HTTP bodies are never logged. Originals and send copies
are intentionally kept in the owner's download directory.
"""
import argparse
import hashlib
import re
import tempfile
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
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


def protocol_settings(request):
    protocol = request.get("protocol", 1)
    if type(protocol) is not int or protocol not in (1, 2, 3):
        raise ValueError("unsupported_protocol")
    if protocol == 3:
        # v3 splits one bot video into separate jobs: "download" keeps the
        # original here; "compress" makes a send copy from a download job of
        # the same bot job (scope). Only job IDs cross the boundary, never paths.
        op = request.get("op")
        if not isinstance(request.get("scope"), str) or not JOB.fullmatch(request["scope"]):
            raise ValueError("invalid_request")
        if op == "download":
            if set(request) - {"protocol", "op", "scope", "url", "deadline"}:
                raise ValueError("invalid_request")
            return None
        if op == "compress":
            if (set(request) - {"protocol", "op", "scope", "source", "max_bytes", "deadline", "settings"} or
                    not isinstance(request.get("source"), str) or not JOB.fullmatch(request["source"])):
                raise ValueError("invalid_request")
            return validate_settings(request.get("settings"))
        raise ValueError("invalid_request")
    if protocol == 2:
        if set(request) - {"url", "max_bytes", "deadline", "protocol", "settings"}:
            raise ValueError("invalid_request")
        return validate_settings(request.get("settings"))
    if "settings" in request:
        raise ValueError("invalid_settings")
    return None


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
    encoder = encoder or ("libx264" if os.name != "nt" else "av1_nvenc")
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
                run(base + ["-c:v", "av1_nvenc", "-preset", {"veryfast": "p3", "faster": "p4", "fast": "p5", "medium": "p6", "slow": "p7"}[settings["preset"]], "-tune", "hq",
                 "-multipass", "fullres" if settings["passes"] == 2 else "disabled", "-rc", "vbr", "-b:v", str(rate),
                 "-maxrate", str(rate * 2), "-bufsize", str(rate * 4),
                 "-rc-lookahead", "32", "-spatial-aq", "1", "-temporal-aq", "1",
                 "-pix_fmt", "p010le", "-c:a", "aac", "-b:a", str(audio_rate or 128000),
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


def seal_file(source, destination, key):
    nonce = secrets.token_bytes(12)
    encrypt = Cipher(algorithms.AES(key), modes.GCM(nonce)).encryptor()
    encrypt.authenticate_additional_data((destination.parent.name + ":" + destination.name).encode())
    temporary = destination.with_suffix(".tmp")
    with source.open("rb") as src, temporary.open("wb") as out:
        out.write(nonce)
        while chunk := src.read(1024*1024):
            out.write(encrypt.update(chunk))
        out.write(encrypt.finalize())
        out.write(encrypt.tag)
        out.flush()
        os.fsync(out.fileno())
    temporary.replace(destination)


def open_file(source, destination, key):
    if source.stat().st_size < 28:
        raise ValueError("invalid_checkpoint")
    with source.open("rb") as src, destination.open("wb") as out:
        nonce = src.read(12)
        src.seek(-16, 2)
        tag = src.read(16)
        length = src.tell() - 28
        src.seek(12)
        decrypt = Cipher(algorithms.AES(key), modes.GCM(nonce, tag)).decryptor()
        decrypt.authenticate_additional_data((source.parent.name + ":" + source.name).encode())
        while length:
            chunk = src.read(min(length, 1024*1024))
            if not chunk:
                raise ValueError("invalid_checkpoint")
            out.write(decrypt.update(chunk))
            length -= len(chunk)
        out.write(decrypt.finalize())


class Jobs:
    """Encrypted checkpoints; one encoder, short authenticated HTTP polls.

    Polls renew a 30-second lease. Loss of bot consent stops work when the
    lease expires; checkpoints survive. Only the bot can authorize sending.
    """
    def __init__(self, root, token, output_root=None):
        self.root = root
        self.directory = output_root or root / "downloads" / "smsbot"
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.key = hashlib.sha256(("smsbot-download-jobs:" + token).encode()).digest()
        self.scratch = self.directory / ".scratch"
        # Clean only this worker's resolved scratch directory after a crash.
        if self.scratch.resolve().parent != self.directory.resolve():
            raise ValueError("invalid_scratch_directory")
        if self.scratch.exists():
            shutil.rmtree(self.scratch)
        self.scratch.mkdir(mode=0o700)
        self.lock = threading.RLock()
        self.active = {}
        self.busy = threading.Lock()

    def path(self, job):
        if not re.fullmatch(r"[a-f0-9]{64}", job):
            raise ValueError("invalid_job")
        path = self.directory / job
        path.mkdir(exist_ok=True, mode=0o700)
        return path

    def save(self, path, value):
        nonce = secrets.token_bytes(12)
        data = nonce + AESGCM(self.key).encrypt(nonce, json.dumps(value).encode(), path.name.encode())
        temporary = path / "state.tmp"
        with temporary.open("wb") as out:
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        temporary.replace(path / "state.enc")

    def load(self, path):
        data = (path / "state.enc").read_bytes()
        return json.loads(AESGCM(self.key).decrypt(data[:12], data[12:], path.name.encode()))

    def poll(self, job, request):
        settings = protocol_settings(request)
        if settings is not None:
            settings["max_mb"] = min(settings["max_mb"], request["max_bytes"] / 1_000_000)
            request = {**request, "settings": settings}
        with self.lock:
            if request.get("op") == "compress":
                self.source(request["source"], request["scope"], job)
            path = self.path(job)
            if (path / "state.enc").exists():
                state = self.load(path)
                if any(state.get(k) != request.get(k) for k in set(state) | set(request) if k not in ("stage",)):
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
            return {"stage": state["stage"], **({"protocol": state["protocol"]} if state.get("protocol") in (2, 3) else {})}

    def source(self, source, scope, job):
        """The completed download job of the same bot job that a compress job reads."""
        if source == job or not (self.directory / source / "state.enc").exists():
            raise ValueError("source_unavailable")
        state = self.load(self.directory / source)
        if (state.get("op") != "download" or state.get("scope") != scope or state["stage"] != "complete" or
                time.time() >= state["deadline"] or not (self.directory / source / "original.enc").exists()):
            raise ValueError("source_unavailable")
        return self.directory / source

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
                # Working files are transient, owner-only; only encrypted files
                # are published as durable checkpoints.
                with tempfile.TemporaryDirectory(prefix="video-", dir=self.scratch) as tmp:
                    folder = Path(tmp)
                    work.folder, work.alive, work.stage = folder, alive, stage
                    deadline = time.monotonic() + max(0, state["deadline"] - time.time())
                    original = folder / "original.mp4"
                    checkpoint = path / "original.enc"
                    if state.get("op") == "compress":
                        with self.lock:
                            source = self.source(state["source"], state["scope"], job)
                        open_file(source / "original.enc", original, self.key)
                    elif checkpoint.exists():
                        open_file(checkpoint, original, self.key)
                    else:
                        stage("downloading")
                        originals = folder / "originals"
                        originals.mkdir()
                        receipt = folder / "receipt.json"
                        python = self.root / "research/.oss-venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
                        run([str(python), str(Path(__file__).with_name("download-source.py")),
                             "--root", str(self.root), state["url"], "--output", str(originals),
                             "--receipt", str(receipt), "--timeout", str(max(1, int(deadline-time.monotonic())))], deadline)
                        files = [Path(f).resolve() for f in json.loads(receipt.read_text())["files"]]
                        if len(files) != 1 or not files[0].is_relative_to(originals.resolve()) or files[0].suffix.lower() != ".mp4":
                            raise ValueError("single_video_required")
                        files[0].replace(original)
                        seal_file(original, checkpoint, self.key)
                    if state.get("op") == "download":
                        # The original stays here for a compress job to read.
                        stage("complete")
                        return
                    stage("compressing")
                    options = {"settings": state["settings"]} if state.get("protocol") in (2, 3) else {}
                    output = compress(original, folder / "send.mp4", state["max_bytes"], deadline, **options)
                    if not alive():
                        raise LeaseExpired()
                    seal_file(output, path / "result.enc", self.key)
                    stage("complete")
        except (TimeoutError, LeaseExpired):
            with self.lock:
                state["stage"] = "expired" if time.time() >= state["deadline"] else "queued"
                self.save(path, state)
        except Exception:
            if state is None:
                return
            with self.lock:
                state["stage"] = "failed"
                self.save(path, state)
        finally:
            work.__dict__.clear()
            with self.lock:
                self.active.pop(job, None)

    def result(self, job, destination):
        with self.lock:
            path = self.path(job)
            state = self.load(path)
            if state["stage"] != "complete" or time.time() >= state["deadline"] or state.get("op") == "download":
                raise ValueError("result_unavailable")
            open_file(path / "result.enc", destination, self.key)
            if not 0 < destination.stat().st_size <= state["max_bytes"]:
                raise ValueError("size_limit")


def create_server(root, token, port, output_root=None):
    jobs = Jobs(root, token, output_root)

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
                    # protocol stays 2 for bots that compare it exactly.
                    data = b'{"protocol":2,"protocols":[1,2,3]}'
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(data)))
                    self.send_header("Cache-Control", "no-store")
                    self.end_headers()
                    self.wfile.write(data)
                    return
                parts = self.path.split("/")
                if len(parts) not in (4, 5) or parts[:3] != ["", "download", "jobs"]:
                    raise ValueError()
                job = parts[3]
                if self.command == "POST" and len(parts) == 4:
                    length = int(self.headers.get("Content-Length", "0"))
                    if not 0 < length <= 16384:
                        raise ValueError()
                    request = json.loads(self.rfile.read(length))
                    if not isinstance(request, dict):
                        raise ValueError()
                    op = request.get("op") if request.get("protocol") == 3 else None
                    if ((op != "compress" and not valid_url(request.get("url"))) or
                        (op != "download" and (type(request.get("max_bytes")) is not int or
                                               not 100000 <= request["max_bytes"] <= LIMIT)) or
                        type(request.get("deadline")) is not int or
                        not time.time()-5 < request["deadline"] <= time.time()+3600):
                        raise ValueError()
                    settings = protocol_settings(request)
                    if settings is not None and request["deadline"] > time.time() + settings["execution_minutes"] * 60 + 5:
                        raise ValueError("invalid_deadline")
                    data = json.dumps(jobs.poll(job, request)).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(data)))
                    self.send_header("Cache-Control", "no-store")
                    self.end_headers()
                    self.wfile.write(data)
                elif self.command == "GET" and len(parts) == 5 and parts[4] == "result":
                    with tempfile.TemporaryDirectory(prefix="result-", dir=jobs.scratch) as tmp:
                        destination = Path(tmp) / "result.mp4"
                        jobs.result(job, destination)
                        self.send_response(200)
                        self.send_header("Content-Type", "video/mp4")
                        self.send_header("Content-Length", str(destination.stat().st_size))
                        self.send_header("Cache-Control", "no-store")
                        self.end_headers()
                        with destination.open("rb") as src:
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

    return ThreadingHTTPServer(("127.0.0.1", port), Handler)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--output-root", type=Path)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--port", type=int, default=8767)
    args = parser.parse_args()
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
    print("Download worker ready on loopback; originals remain in the configured downloads folder.", flush=True)
    create_server(args.root.resolve(), worker_token, args.port, output_root=args.output_root).serve_forever()
