# Server-local download command

Create **Commands → New → Download video**, keep the name `download`, select who
can use it, enable it and save. Send `!download <share text containing one video
link>`. One Douyin or Xiaohongshu video is supported per request; photo galleries
and multiple distinct links are rejected. Preview recognizes links offline.

All downloading, compression and sending preparation run on the SmsBot server.
The helper uses the existing Rednote engine through an anonymous adapter, without
importing Chrome cookies or another application's vault. Login-only content may fail.

## Server installation

On Acer (Linux), from a checkout containing the new scripts:

```sh
python3 scripts/install-download-runtime.py --start-service
```

The installer requires Python 3.12+, system pip, internet access and user systemd.
It needs no sudo. It installs:

- Engine and virtual environment: `~/.local/lib/smsbot/downloader/research`.
- Worker scripts: `~/.local/lib/smsbot/downloader/worker`.
- FFmpeg/FFprobe: `~/.local/lib/smsbot/downloader/bin`.
- Encrypted originals, send copies and state: `~/.local/share/smsbot/downloads/<random job>`.
- Worker token: `~/.local/share/smsbot/vault/download-worker-token`.
- User service: `smsbot-downloader.service`, listening only on `127.0.0.1:8767`.

No Windows worker or SSH tunnel is required. Rednote's source is pinned to
`106966f552e45eb3af75055810ab51f940f767be`. The installer records resolved Python
dependencies (including checkpoint encryption with `cryptography`) and FFmpeg's URL/checksum and installs bundled Chromium. It does not
restart or reconfigure SmsBot itself.

After deploying the new command code, append these flags to SmsBot's existing
service invocation:

```text
--download-endpoint http://127.0.0.1:8767/download --download-token-file /home/deusical/.local/share/smsbot/vault/download-worker-token
```

Restart using the normal deployment workflow. Automation restarts paused.
Inspect the helper with `systemctl --user status smsbot-downloader.service`.
It runs at lower priority, limited to 150% CPU and 1536 MiB RAM, and accepts one
request at a time.

## Compression and bounds

`media.video@1` exposes bounded settings in Commands:

| Setting | Default | Allowed values |
| --- | --- | --- |
| `preset` | `slow` | `veryfast`, `faster`, `fast`, `medium`, `slow` |
| `passes` | 2 | 1?2 |
| `max_attempts` | 3 | 1?3 |
| `copy_if_fits` | true | boolean |
| `max_mb` | 19.99 | 0.1?19.99 decimal MB, further capped by host attachment limits |
| `execution_minutes` | 15 | 1?60 |

Defaults preserve slow two-pass encoding, three attempts, copying an original
that fits, and the existing 15-minute lifetime. The byte target is now explicitly
19.99 decimal MB, capped by both the per-file and combined attachment limits.
The default host limit is 10 MiB per file. Configured and effective values are
shown separately when the host lowers the command target. No encoder name,
thread count, path or shell flag is accepted in command settings.

Linux uses H.264 (`libx264`), AAC and two host-controlled threads. One pass omits
the pass log and first analysis pass; two passes preserve the existing analysis
and encoding flow. Windows uses the existing AV1 NVENC/HQ path. Presets map to
NVENC p3/p4/p5/p6/p7 respectively; one pass maps to `-multipass disabled`, two
to `-multipass fullres` inside a single NVENC invocation. Attempts and
`copy_if_fits` apply equally to CPU and NVENC. The UI cost line is a worst-case
encode-work estimate; hardware multipass is not multiple FFmpeg processes.

Every encoded result targets 98.5% of the byte budget and retries overshoots up
to `max_attempts`. Size, duration, audio and a full verification decode remain
mandatory, including copied files. Originals stay separate. A long video can
take tens of minutes; the editor warns when the selected deadline looks short.
The estimate is advisory and cannot extend a deadline.

Incoming messages must still arrive within two minutes. At acceptance the bot
snapshots effective settings, byte ceiling and the absolute execution deadline.
The deadline includes approval, queueing, preparation and upload. Approval still
expires after two minutes. Editing settings cannot alter accepted jobs; a host
limit reduction cancels a job whose snapshotted ceiling is no longer allowed.
Legacy flat `execution_minutes` maps into settings; zero/absent in the legacy
representation means 15. Explicit modern settings require 1?60 minutes.

## Worker protocol v2

The bot probes authenticated `GET /download/capabilities` for `{"protocol":2}`
before creating a worker job. `POST /download/jobs/<job>` then includes:

```json
{"protocol":2,"url":"https://douyin.com/example","max_bytes":19990000,"deadline":1800000000,"settings":{"preset":"slow","passes":2,"max_attempts":3,"copy_if_fits":true,"max_mb":19.99,"execution_minutes":15}}
```

The worker independently validates all enums and bounds, including the deadline
window and byte ceiling. The encrypted checkpoint stores all effective settings;
a restart uses those settings, even if code defaults changed. Changed settings
for an existing job are rejected. The host's single preparation slot and CPU
thread count stay outside commands. Legacy protocol requests remain supported.

If the worker does not advertise protocol 2, the bot submits a legacy request
and lets that worker use its defaults, as approved by the owner. It logs only
`download_worker_outdated_using_defaults` once per downloader instance, with no
URL, content, contact or upstream error. The warning currently appears in the
service log; Activity continues to show the sanitized preparation stage. The
original deadline, byte ceiling and every dispatcher safety check still apply.
Network failures yield another poll and do not bypass authorization.

The bot stores its job ID and encrypted plan immediately. Dispatch enqueues or
polls preparation with short HTTP calls and yields between polls, so other
commands can run. Activity shows the last preparation stage: queued, downloading,
compressing, verifying, uploading or waiting for a worker/upload slot. One
background upload runs independently of command dispatch, under the remaining
execution deadline. Only dispatch can commit its result after repeating every
safety check, persist outbound intent, and invoke the send pipeline.

The worker has one preparation slot. It checkpoints the downloaded original and
verified send copy with AES-GCM encryption; URL and job metadata are encrypted
too. A worker restart resumes an active job from its saved original on the next
bot poll. A complete result can be fetched again without repeating preparation.
Encoding interrupted before verification restarts encoding from the original.
Transient plaintext processing files live in an owner-only scratch directory,
are removed after use, and are cleared on worker startup after a crash. Retained
checkpoints are under the job ID directory; keep the worker token to read them.
The token is outside source and also derives the checkpoint encryption key.

Every poll renews a 30-second preparation lease. If the bot pauses, cancels, loses
consent, or stops polling, the lease expires and stops running preparation.
Subprocesses are also stopped at the absolute deadline, or after 120 seconds
without file growth or advancing encoder frames/media time. Stalled preparation
fails; only lost leases leave resumable work. There is no promise that an upstream
site's own network timeout or failure will disappear.

**SmsBot restart still starts paused and cancels unsent jobs.** Retained worker
checkpoints do not authorize restarting a cancelled bot job or sending its result.
Worker restart recovery applies while the original bot job remains authorized.
Pause, enrollment changes and final recipient checks always prevent sending.
Uncertain sends are never retried automatically.

`download_worker_unavailable` means the helper is not configured;
`download_failed` means preparation failed or returned an invalid/oversized file.
Worker responses never contain raw upstream errors, file titles or credentials.

## Helper protocol

Upgrade both bot and worker together. The old synchronous `POST /download`
returns 410. Keep the bot endpoint flag ending in `/download`; the client appends
`/jobs/<persisted bot job ID>`.

`POST /download/jobs/<64 lowercase hex characters>` on loopback requires
`Authorization: Bearer <token>` and JSON:

```json
{"url":"https://v.douyin.com/example/","max_bytes":19999999,"deadline":1793500000}
```

`deadline` is the accepted job's absolute Unix timestamp, at most one hour ahead.
Repeated requests are idempotent and renew the lease; URL, size ceiling and deadline
cannot change for an existing ID. Success returns JSON `{"stage":"downloading"}`
(or another stage). `GET /download/jobs/<id>/result` returns the verified MP4 only
when complete and still within its deadline. Responses use `Cache-Control: no-store`.
Authentication failures return 401; invalid input and preparation failures use
sanitized responses. Temporary request failures are polled again under the same
job ID until expiry. No upstream send is retried by this protocol.

## Verification

```sh
go test ./...
go vet ./...
python3 -B -m unittest discover -s scripts -p test_download_worker.py
```

Fixtures cover extraction, authentication, byte limits, admission freshness,
immutable deadlines, dispatch fairness, pause, enrollment, expiry, checkpoint
recovery, progress watchdogs and uncertain sends. Generated media exercises real
two-pass encoding, full decoding and the already-under-cap path.
End-to-end live downloads and sends remain a designated-conversation test.

Dependency sources: [Rednote](https://github.com/SeanLi-Coder/rednote_downloader),
[FFmpeg builds](https://github.com/BtbN/FFmpeg-Builds/releases), and
[Playwright browser installation](https://playwright.dev/python/docs/browsers).
