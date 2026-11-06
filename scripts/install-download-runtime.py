"""Install the server-local downloader in the current Linux user's home.

No root access, browser credentials or SmsBot restart required. The upstream
source is pinned; FFmpeg's selected asset and SHA256 are recorded on installation.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import urllib.request

COMMIT = "106966f552e45eb3af75055810ab51f940f767be"


def get(url, target):
    request = urllib.request.Request(url, headers={"User-Agent": "smsbot-downloader-setup"})
    with urllib.request.urlopen(request, timeout=120) as source, target.open("wb") as out:
        shutil.copyfileobj(source, out)


def checked(args):
    result = subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if result.returncode:
        raise SystemExit("Dependency setup failed: " + str(args[0]))


def main():
    if not sys.platform.startswith("linux"):
        raise SystemExit("Run this installer on the Linux SmsBot host.")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--start-service", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    home = Path.home()
    root = home / ".local/lib/smsbot/downloader"
    source = root / "research/rednote_downloader"
    root.mkdir(parents=True, exist_ok=True)
    if not source.exists():
        archive = root / "source.tar.gz"
        get("https://codeload.github.com/SeanLi-Coder/rednote_downloader/tar.gz/" + COMMIT, archive)
        with tarfile.open(archive) as tar:
            prefix = tar.getmembers()[0].name.split("/")[0]
            staging = root / "source-unpack"
            staging.mkdir(exist_ok=True)
            tar.extractall(staging, filter="data")
        source.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(staging / prefix), source)
        staging.rmdir()
        archive.unlink()
    (root / "source-commit.txt").write_text(COMMIT + "\n")
    python = root / "research/.oss-venv/bin/python"
    if not python.exists():
        checked([sys.executable, "-m", "venv", "--without-pip", str(python.parent.parent)])
    print("Checking isolated Python dependencies...", flush=True)
    checked([sys.executable, "-m", "pip", "--python", str(python), "install", "-r", str(source / "requirements.txt")])
    print("Installing full Chromium (headless shell is not used)...", flush=True)
    checked([str(python), "-m", "playwright", "install", "chromium", "--no-shell"])
    print("Checking FFmpeg and writing local service...", flush=True)
    frozen = subprocess.check_output([sys.executable, "-m", "pip", "--python", str(python), "freeze"])
    (root / "installed-requirements.txt").write_bytes(frozen)
    binary_dir = root / "bin"
    binary_dir.mkdir(exist_ok=True)
    if not all((binary_dir / name).exists() for name in ("ffmpeg", "ffprobe")):
        request = urllib.request.Request("https://api.github.com/repos/BtbN/FFmpeg-Builds/releases/latest", headers={"User-Agent": "smsbot-downloader-setup"})
        with urllib.request.urlopen(request, timeout=30) as response:
            release = json.load(response)
        asset = next(a for a in release["assets"] if a["name"] == "ffmpeg-n8.1-latest-linux64-gpl-8.1.tar.xz")
        archive = root / "ffmpeg.tar.xz"
        get(asset["browser_download_url"], archive)
        with archive.open("rb") as incoming:
            digest = hashlib.file_digest(incoming, "sha256").hexdigest()
        if asset.get("digest") and asset["digest"] != "sha256:" + digest:
            raise SystemExit("FFmpeg checksum mismatch.")
        with tarfile.open(archive) as tar:
            for name in ("ffmpeg", "ffprobe"):
                member = next(m for m in tar.getmembers() if m.isfile() and m.name.endswith("/bin/" + name))
                with tar.extractfile(member) as incoming, (binary_dir / name).open("wb") as out:
                    shutil.copyfileobj(incoming, out)
                (binary_dir / name).chmod(0o755)
        archive.unlink()
        (root / "ffmpeg-build.json").write_text(json.dumps({"url": asset["browser_download_url"], "sha256": digest}))
    worker = root / "worker"
    worker.mkdir(exist_ok=True)
    # The worker, its downloader, and any task plugins (task_*.py) beside it.
    names = ["download-worker.py", "download-source.py"] + sorted(p.name for p in Path(__file__).parent.glob("task_*.py"))
    for name in names:
        if Path(__file__).with_name(name).resolve() != (worker / name).resolve():
            shutil.copy2(Path(__file__).with_name(name), worker / name)
    downloads = home / ".local/share/smsbot/downloads"
    downloads.mkdir(parents=True, exist_ok=True)
    token = home / ".local/share/smsbot/vault/download-worker-token"
    token.parent.mkdir(parents=True, exist_ok=True)
    unit_dir = home / ".config/systemd/user"
    unit_dir.mkdir(parents=True, exist_ok=True)
    def quote(path):
        return '"' + str(path).replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
    # Leave one core to SmsBot and the rest of the host; Nice=10 already gives
    # them priority. A fixed 150% throttled x264 to 1.5 of the Acer's 4 cores.
    quota = max(100, ((os.cpu_count() or 2) - 1) * 100)
    unit = f'''[Unit]
Description=SmsBot server-local video downloader
After=network-online.target

[Service]
ExecStart={quote(python)} {quote(worker / 'download-worker.py')} --root {quote(root)} --output-root {quote(downloads)} --token-file {quote(token)}
Environment="PATH={binary_dir}:/usr/bin:/bin"
UMask=0077
Nice=10
CPUQuota={quota}%
MemoryMax=1536M
KillMode=control-group
TimeoutStopSec=10
Restart=on-failure

[Install]
WantedBy=default.target
'''
    (unit_dir / "smsbot-downloader.service").write_text(unit)
    checked(["systemctl", "--user", "daemon-reload"])
    if args.start_service:
        checked(["systemctl", "--user", "enable", "--now", "smsbot-downloader.service"])
    print("Server-local downloader installed. SmsBot itself was not restarted or reconfigured.")


if __name__ == "__main__":
    main()
