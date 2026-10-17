# Local history and encrypted recovery

The inbox is locally retained history, not a claim of complete Google Messages history. Historical imports and restored records never enter command routing. Pagination orders by timestamp and message ID, with an opaque cursor. Search decrypts at most 100 retained records per page in memory; keep following `next_cursor` even if a search page contains no matches. No plaintext search index is created.

Retention accepts zero (disabled) or 1–3650 days. Expiry removes old message records and clears old terminal job plans. Durable incoming IDs and uncertain outbound transaction evidence remain, preventing expired messages or backups from enabling automatic replay. Active queued, processing, approval and submitting plans are not expired mid-operation. SQLite expiry is logical deletion; it does not promise forensic erasure of filesystem snapshots or old backups.

Backup and restore are offline operations: stop the service and hold its exclusive state-directory lock. The backup is authenticated AES-GCM ciphertext sealed under the existing vault key and the `backup` scope. It includes enrollment, message and attachment ciphertext, job evidence, the provider usage ledger and explicitly allowed configuration settings. Usage is retained so recovery does not reset budget accounting. Provider keys, owner credentials, browser sessions, opt-in memory and Google pairing/cookies are excluded. Backups contain sensitive enrolled identities and encrypted message payloads; keep them private outside the repository.

Restore requires a fresh initialized database with no enrollment, jobs or messages and the same vault key. Local destination credentials remain local. It rejects unsupported backup versions, newer database schemas, tampering and invalid message ciphertext, and commits atomically. The restored bot starts paused. Queued/generating/processing/approval work is cancelled, interrupted submitting work becomes `unknown_submission`, and every saved plan is removed. Restore does not submit messages or authorize retry of uncertain sends. Review enrollment and supply excluded credentials locally before enabling.

The vault key is essential: a backup alone cannot recover messages. On Windows, key files are protected using CurrentUser DPAPI; copying that key file to another Windows account or Linux will not make it readable. Linux uses its own local key storage. This feature does not export an unprotected portable recovery key or automatically migrate Windows identity/platform protection. A same-identity recovery using the original vault is supported; cross-identity/platform recovery needs an explicitly designed key-transfer mechanism.

The current bounded backup format supports up to 128 MiB of logical JSON (256 MiB maximum supplied ciphertext). Locally uploaded bytes are included; remote attachment references and keys are included, but remote bytes that were only viewed on demand are not archived. Use the streaming message archive below for large history/file collections; the operational command fails rather than silently omitting records.

## Commands

Stop the service cleanly first. Use the same `--data` and original `--key` paths:

```powershell
.\bin\smsbot.exe --data YOUR_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --backup YOUR_NEW_BACKUP_PATH
```

The output file must be new. For restore, choose a fresh state directory, use the same readable original key, and create a new local owner credential before restoring:

```powershell
.\bin\smsbot.exe --data YOUR_NEW_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --init --owner-file YOUR_NEW_OWNER_FILE
.\bin\smsbot.exe --data YOUR_NEW_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --restore YOUR_BACKUP_PATH
```

Initialization reuses an existing protected key only for a new state database; it does not replace that key. A running state directory is locked against backup, restore and cookie updates. Reconfigure the excluded provider/Google credentials locally afterward.


## Original files and streaming message archives

The Archive page contains **Original file archive** and **Message history
export**. Original preservation defaults to enabled for conversations with collection
consent, with a 10 GiB disk quota and no per-file application cap. This is independent
of bot replies, sending pause and viewing/upload limits. Set either limit to zero for
unlimited. Each remote attempt has a 90-second budget; interrupted downloads restart
from the beginning after recovery. Files show saved, pending, failed, unavailable or
policy-limited state. Transient failures use retry backoff; originals with no source wait for source updates. Policy-limited files wait for changed limits or available quota space. Exact identity/consent checks apply. Expiry
removes file references; periodic garbage collection removes unreferenced originals.
Saved originals support authenticated local preview, download and byte ranges while
the phone is offline or collection is stopped. Thumbnails never replace originals.

Export all archives or one conversation, optionally from a date. A database snapshot
keeps records consistent while capture continues. Collection of text remains active;
file publication and garbage collection wait for the export. The snapshot and file
staging require additional disk space beyond the original-file quota. Only the latest
server package is kept; starting another removes the previous package. Jobs can be
cancelled, and an interrupted export is explicitly marked after restart. Download the
completed package, then use **Delete server export package** to remove its server copy.

Choose **Encrypted backup** for recovery with the original protected key, or
**Unencrypted readable export** for a TAR containing UTF-8 JSON records under
`records/`, original bytes under `files/`, and a final `manifest.json` with counts and
integrity digest. Readable records include messages and observed text revisions;
pairing credentials, provider keys and media decryption keys are excluded. A readable
export can be opened without the server key and contains private messages/files. It
is an interchange export, not an input to the encrypted restore command.

Streaming encrypted archives use authenticated ordered 64 KiB chunks, a final marker,
and a manifest covering records and original-file ciphertext. They have no 128 MiB
aggregate limit; each encrypted metadata record is limited to 16 MiB. Original bytes
stream separately. Missing/corrupt originals are explicitly counted, not silently
reported as backed up. Backups cover locally retained data; they do not establish
complete upstream history or recover unobserved intermediate edits.

Stop the service for CLI export or recovery. The existing exclusive state lock applies:

```powershell
.\bin\smsbot.exe --data YOUR_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --archive-backup YOUR_NEW_ARCHIVE.sma
.\bin\smsbot.exe --data YOUR_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --archive-backup YOUR_NEW_EXPORT.tar --archive-plaintext
```

Both commands accept `--archive-conversation CONVERSATION_ID` and
`--archive-since 2026-01-01T00:00:00Z`. Output must be a new file.

Initialize fresh state with the same readable protected key, then restore offline:

```powershell
.\bin\smsbot.exe --data YOUR_NEW_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --init --owner-file YOUR_NEW_OWNER_FILE
.\bin\smsbot.exe --data YOUR_NEW_STATE_DIRECTORY --key YOUR_ORIGINAL_KEY_PATH --archive-restore YOUR_ARCHIVE.sma
```

Offline restore uses available filesystem capacity rather than the background acquisition quota, so a fresh destination can recover archives exceeding the default 10 GiB download quota. The policy still limits later background downloads.

Restore stages and verifies the whole package, manifest, ciphertext and original-file
hashes before committing records. A failure leaves destination records unchanged.
The destination must have no existing archive/messages/enrollment/job/memory evidence;
use fresh state. Restore does not bring over pairing, providers, credentials, sending
jobs or collection consent. Archives are stopped and sending is paused. Review and
enable collection locally if desired. Existing Windows DPAPI identity limitations
still apply; encrypted archives are not a portable key-transfer mechanism.

How the local archive stays current is described in [DESIGN_NOTES.md](DESIGN_NOTES.md#local-history-synchronization).
