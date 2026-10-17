# Known limitations

SmsBot is an experimental, self-hosted tool. This page says plainly what is unverified,
unsupported or risky so you can decide whether it suits you.

## Risks of the approach

- **Unofficial client.** The Google adapter is a reverse-engineered implementation of the
  Messages for web protocol. Google can change it, rate-limit it or block it without
  notice, and using it may conflict with Google's terms of service. Use your own account
  and accept that risk.
- **Session lifetime.** Google may reject the saved cookies (HTTP 401) at times. The
  service pauses and reports it; recover with the offline cookie update described in
  [Getting started](GETTING_STARTED.md). Long-term cookie behavior is only anecdotally
  observed, not guaranteed.
- **No independent audit.** The code has been reviewed by its author and by AI
  reviewers, not by an external security professional.

## What has and has not been verified live

Automated tests are offline and synthetic. They do not contact Google, carriers or AI
providers. The following behaviors depend on real services and were exercised only in
limited, owner-run checks, or not at all:

- Delivery on a locked or backgrounded phone, SMS versus RCS differences, and behavior
  with competing paired clients.
- Sent, Delivered and Read statuses, read marking, and typing indicators against real
  Google conversations.
- Sending and downloading MMS or RCS files, and very large files.
- Live AI generation against a paid provider; only mock providers are tested.
- Continuous operation over days and recovery after Google outages.

Treat any of these as unproven until you have tried them on a designated test
conversation.

## Product limits

- **Message actions depend on protocol.** Native quoted replies and Unicode emoji
  reactions are implemented for eligible collected RCS messages, with synthetic
  offline verification. Live reply/reaction interoperability remains pending owner
  testing. SMS/MMS equivalents and image reactions are not implemented. Messages
  without known upstream identity/protocol cannot be action targets until refreshed.
- **Outbound editing is unavailable.** The pinned companion library exposes no
  outbound edit operation; Google documents editing as phone-app-only. The dashboard
  retains and displays observed changes, but cannot edit a sent message on the phone.
- **Reaction ordering is best effort.** Snapshots whose reads started before a newer
  local observation are rejected. The companion protocol supplies no reliable action
  version, so delayed upstream events and stale upstream snapshots cannot always be
  distinguished. A submitted or uncertain reaction blocks conflicting changes until
  a later snapshot shows its intended state; check the phone if it remains unresolved.

- **Individual conversations only.** Groups are never used.
- **History is best effort.** The archive stores what Google makes available. An
  exhausted history cursor does not prove the history is complete, edits are only
  captured if they were observed or covered by a scan, and a message missing from a
  page is never treated as deleted.
- **One owner, one phone.** There is a single owner credential, no multi-user support
  and no credential-rotation screen.
- **Sends cannot be undone.** Pausing stops further sends; it cannot recall a message
  already submitted. An uncertain submission is never retried, so you must check the
  phone.
- **Read receipts reach other people.** With the default setting, viewing an unread
  conversation marks it read on your phone; whether the other person sees a receipt
  depends on your phone's own setting. Outgoing typing indicators are not sent.
- **Attachments buffer in memory.** Uploads and downloads are held in memory while
  processed, so "unlimited" size settings are bounded by host memory, storage, SQLite,
  and Google or carrier limits.
- **Polling, not push.** The dashboard polls the service (every few seconds, and only
  when the list changes). Typing is near-real-time but not instant.
- **Windows and Linux.** Windows key protection uses DPAPI; macOS is untested.
- **No native mobile app.** The dashboard installs as a phone app (PWA) over HTTPS. The
  former Android client was removed.

## Operational notes

- Every start is paused and cancels queued work; automatic restarts restore the
  connection but do not re-enable replies.
- Backups need the original master key. Without it the data cannot be recovered.
- Keep the service on a private network. Do not expose it to the public internet.
