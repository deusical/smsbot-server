# Local history and encrypted recovery

The inbox is locally retained history, not a claim of complete Google Messages history. Historical imports and restored records never enter command routing. Pagination orders by timestamp and message ID, with an opaque cursor. Search decrypts at most 100 retained records per page in memory; keep following `next_cursor` even if a search page contains no matches. No plaintext search index is created.

Retention accepts zero (disabled) or 1–3650 days. Expiry removes old message records and clears old terminal job plans. Durable incoming IDs and uncertain outbound transaction evidence remain, preventing expired messages or backups from enabling automatic replay. Active queued, processing, approval and submitting plans are not expired mid-operation. SQLite expiry is logical deletion; it does not promise forensic erasure of filesystem snapshots or old backups.

Backup and restore are offline operations: stop the service and hold its exclusive state-directory lock. The backup is authenticated AES-GCM ciphertext sealed under the existing vault key and the `backup` scope. It includes enrollment, message and attachment ciphertext, job evidence, the provider usage ledger and explicitly allowed configuration settings. Usage is retained so recovery does not reset budget accounting. Provider keys, owner/client credentials, browser sessions, opt-in memory and Google pairing/cookies are excluded. Backups contain sensitive enrolled identities and encrypted message payloads; keep them private outside the repository.

Restore requires a fresh initialized database with no enrollment, jobs or messages and the same vault key. Local destination credentials remain local. It rejects unsupported backup versions, newer database schemas, tampering and invalid message ciphertext, and commits atomically. The restored bot starts paused. Queued/generating/processing/approval work is cancelled, interrupted submitting work becomes `unknown_submission`, and every saved plan is removed. Restore does not submit messages or authorize retry of uncertain sends. Review enrollment and supply excluded credentials locally before enabling.

The vault key is essential: a backup alone cannot recover messages. On Windows, key files are protected using CurrentUser DPAPI; copying that key file to another Windows account or Linux will not make it readable. Linux uses its own local key storage. This feature does not export an unprotected portable recovery key or automatically migrate Windows identity/platform protection. A same-identity recovery using the original vault is supported; cross-identity/platform recovery needs an explicitly designed key-transfer mechanism.

The current bounded backup format supports up to 128 MiB of logical JSON (256 MiB maximum supplied ciphertext). Locally uploaded bytes are included; remote attachment references and keys are included, but remote bytes that were only viewed on demand are not archived. Large installations must reduce retained history or adopt a future streaming backup format; the command fails rather than silently omitting records.

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
