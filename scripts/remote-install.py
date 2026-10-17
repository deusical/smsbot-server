#!/usr/bin/env python3
"""Install an uploaded SmsBot release on the Linux host that runs it (user systemd unit).

Run on the host by scripts/deploy-remote.ps1:
  python3 remote-install.py LABEL EXPECTED_SHA256 PREVIOUS_SHA256 ORIGIN_HOST

The release is expected at ~/.local/lib/smsbot/smsbot-LABEL.gz. The script verifies
it, backs up the database and encrypted Google session while the service is
stopped, swaps the binary, restarts, checks health, and restores the previous
binary and state if anything fails. It never reads the key, credentials, message
bodies or contacts. Sending stays paused: every start is paused.
"""
import gzip
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

label, expected, previous, origin_host = sys.argv[1:5]
home = Path.home()
root = home / '.local/lib/smsbot'
state = home / '.local/share/smsbot/state'
target = root / 'smsbot'
upload = root / f'smsbot-{label}.gz'
staged = root / f'smsbot.{label}.staged'
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
backup = root / f'smsbot.before-{label}-{stamp}'
recovery = home / '.local/share/smsbot/backups' / f'before-{label}-{stamp}'
stopped = installed = False


def service(operation):
    subprocess.run(['systemctl', '--user', operation, 'smsbot.service'], check=True,
                   capture_output=True, timeout=150)


try:
    os.umask(0o077)
    data = gzip.decompress(upload.read_bytes())
    if data[:4] != b'\x7fELF' or hashlib.sha256(data).hexdigest() != expected:
        raise RuntimeError('upload_checksum')
    if hashlib.sha256(target.read_bytes()).hexdigest() != previous:
        raise RuntimeError('existing_binary_changed')
    with staged.open('xb') as out:
        out.write(data)
        out.flush()
        os.fsync(out.fileno())
    staged.chmod(0o755)
    shutil.copy2(target, backup)
    recovery.mkdir(parents=True, exist_ok=False)
    service('stop')
    stopped = True
    # SQLite's backup API includes the WAL and retains all encrypted settings.
    with sqlite3.connect((state / 'smsbot.db').as_uri() + '?mode=ro', uri=True) as source:
        with sqlite3.connect(recovery / 'smsbot.db') as destination:
            source.backup(destination)
    session = state / 'google-session.enc'
    if session.exists():
        shutil.copy2(session, recovery / session.name)
    os.replace(staged, target)
    installed = True
    service('start')
    subprocess.run(['systemctl', '--user', 'is-active', '--quiet', 'smsbot.service'],
                   check=True, capture_output=True, timeout=10)
    version = subprocess.run([str(target), '--version'], check=True, capture_output=True,
                             text=True, timeout=10).stdout.strip()
    healthy = False
    deadline = time.monotonic() + 40
    while time.monotonic() < deadline:
        try:
            request = urllib.request.Request('http://127.0.0.1:8787/healthz', headers={'Host': origin_host})
            with urllib.request.urlopen(request, timeout=5) as response:
                healthy = response.status == 200
            if healthy:
                break
        except OSError:
            pass
        time.sleep(1)
    if not healthy:
        raise RuntimeError('health_failed')
    # Reporting only: a failed read here must not roll back a healthy service.
    schema = paused = None
    try:
        with sqlite3.connect((state / 'smsbot.db').as_uri() + '?mode=ro', uri=True, timeout=5) as db:
            schema = db.execute('PRAGMA user_version').fetchone()[0]
            row = db.execute("SELECT value FROM settings WHERE key='paused'").fetchone()
            paused = row[0] if row else None
    except sqlite3.Error:
        pass
    print(json.dumps({'deployment': 'installed', 'version': version, 'health': healthy,
                      'schema': schema, 'paused': None if paused is None else paused == 'true',
                      'sha256': hashlib.sha256(target.read_bytes()).hexdigest(),
                      'rollback_binary': backup.name, 'state_backup': str(recovery)}))
except Exception as error:
    reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    staged.unlink(missing_ok=True)
    restored = False
    if installed:
        try:
            service('stop')
            # Restore the matching pre-migration database together with the binary.
            shutil.copy2(backup, staged)
            os.replace(staged, target)
            for suffix in ('-wal', '-shm'):
                (state / ('smsbot.db' + suffix)).unlink(missing_ok=True)
            shutil.copy2(recovery / 'smsbot.db', state / 'smsbot.db')
            if (recovery / 'google-session.enc').exists():
                shutil.copy2(recovery / 'google-session.enc', state / 'google-session.enc')
            service('start')
            restored = True
        except Exception:
            pass
    elif stopped:
        try:
            service('start')
            restored = True
        except Exception:
            pass
    print(json.dumps({'deployment': 'failed', 'reason': reason, 'restored_previous_service': restored}))
    sys.exit(1)
