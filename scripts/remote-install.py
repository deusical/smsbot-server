#!/usr/bin/env python3
"""Install an uploaded SmsBot release on the Linux host that runs it (user systemd unit).

Run on the host by scripts/deploy-remote.ps1:
  python3 remote-install.py LABEL EXPECTED_SHA256 PREVIOUS_SHA256 ORIGIN_HOST

The release is expected at ~/.local/lib/smsbot/smsbot-LABEL.gz. The script verifies
it, backs up the database and encrypted Google session while the service is
stopped, swaps the binary, restarts, checks health, and restores the previous
binary and state if anything fails. When ~/.local/lib/smsbot/downloader/worker
exists, it also atomically updates staged worker scripts there and restarts
smsbot-downloader.service if installed. It never reads the key, credentials,
message bodies or contacts. Sending stays paused: every start is paused.
"""
import contextlib
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


def service(operation):
    subprocess.run(['systemctl', '--user', operation, 'smsbot.service'], check=True,
                   capture_output=True, timeout=150)


def update_worker(root, label, stamp):
    """Atomically install staged worker scripts when the local worker exists.

    Never raises: a worker update failure is reported so it cannot roll back or
    fail a healthy server deployment.
    """
    worker_dir = root / 'downloader' / 'worker'
    staging_dir = root / f'worker-staging-{label}'
    if not worker_dir.is_dir():
        shutil.rmtree(staging_dir, ignore_errors=True)
        return {'updated': False, 'restarted': False}

    temp_files = []
    replaced = False
    backup_name = None
    try:
        if not staging_dir.is_dir():
            raise RuntimeError('worker_staging_missing')
        required = ['download-worker.py', 'download-source.py']
        for name in required:
            if not (staging_dir / name).is_file():
                raise RuntimeError('worker_staging_incomplete')
        names = required + sorted(p.name for p in staging_dir.glob('task_*.py') if p.is_file())

        staged_pairs = []
        for name in names:
            tmp = worker_dir / f'.{name}.{label}.{stamp}.staged'
            tmp.unlink(missing_ok=True)
            temp_files.append(tmp)
            data = (staging_dir / name).read_bytes()
            with tmp.open('xb') as out:
                out.write(data)
                out.flush()
                os.fsync(out.fileno())
            tmp.chmod(0o600)
            staged_pairs.append((name, tmp))

        current_worker = worker_dir / 'download-worker.py'
        if current_worker.is_file():
            backup_path = worker_dir / f'download-worker.py.before-{label}-{stamp}'
            shutil.copy2(current_worker, backup_path)
            backup_name = backup_path.name

        for name, tmp in staged_pairs:
            os.replace(tmp, worker_dir / name)
            replaced = True

        if backup_name is not None:
            for old in worker_dir.glob('download-worker.py.before-*'):
                if old.name != backup_name and old.is_file():
                    old.unlink(missing_ok=True)

        probe = subprocess.run(['systemctl', '--user', 'cat', 'smsbot-downloader.service'],
                               capture_output=True, timeout=30)
        if probe.returncode == 0:
            subprocess.run(['systemctl', '--user', 'restart', 'smsbot-downloader.service'],
                           check=True, capture_output=True, timeout=150)
            restarted = True
        else:
            restarted = False
        return {'updated': replaced, 'restarted': restarted, 'backup': backup_name}
    except Exception as error:
        reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        return {'updated': replaced, 'restarted': False, 'backup': backup_name, 'error': reason}
    finally:
        for tmp in temp_files:
            tmp.unlink(missing_ok=True)
        shutil.rmtree(staging_dir, ignore_errors=True)


def install_release(label, expected, previous, origin_host, home=None):
    home = Path.home() if home is None else Path(home)
    root = home / '.local/lib/smsbot'
    state = home / '.local/share/smsbot/state'
    target = root / 'smsbot'
    upload = root / f'smsbot-{label}.gz'
    staged = root / f'smsbot.{label}.staged'
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    backup = root / f'smsbot.before-{label}-{stamp}'
    recovery = home / '.local/share/smsbot/backups' / f'before-{label}-{stamp}'
    stopped = installed = False

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
        with contextlib.closing(sqlite3.connect((state / 'smsbot.db').as_uri() + '?mode=ro', uri=True)) as source:
            with contextlib.closing(sqlite3.connect(recovery / 'smsbot.db')) as destination:
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
        # Reporting and worker update only: failures here must not roll back a healthy service.
        schema = paused = None
        try:
            with contextlib.closing(sqlite3.connect((state / 'smsbot.db').as_uri() + '?mode=ro', uri=True, timeout=5)) as db:
                schema = db.execute('PRAGMA user_version').fetchone()[0]
                row = db.execute("SELECT value FROM settings WHERE key='paused'").fetchone()
                paused = row[0] if row else None
        except sqlite3.Error:
            pass
        worker = update_worker(root, label, stamp)
        payload = {
            'deployment': 'installed',
            'version': version,
            'health': healthy,
            'schema': schema,
            'paused': None if paused is None else paused == 'true',
            'sha256': hashlib.sha256(target.read_bytes()).hexdigest(),
            'rollback_binary': backup.name,
            'state_backup': str(recovery),
            'worker_updated': worker['updated'],
            'worker_restarted': worker['restarted'],
        }
        if worker.get('backup'):
            payload['worker_backup'] = worker['backup']
        if worker.get('error'):
            payload['worker_error'] = worker['error']
        return 0, payload
    except Exception as error:
        reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        staged.unlink(missing_ok=True)
        shutil.rmtree(root / f'worker-staging-{label}', ignore_errors=True)
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
        return 1, {'deployment': 'failed', 'reason': reason, 'restored_previous_service': restored,
                   'worker_updated': False}


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    label, expected, previous, origin_host = args[:4]
    code, payload = install_release(label, expected, previous, origin_host)
    print(json.dumps(payload))
    return code


if __name__ == '__main__':
    sys.exit(main())
