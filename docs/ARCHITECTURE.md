# Architecture

SmsBot is a single Go service with an embedded React dashboard. It pairs with your
Google Messages account the way Messages for web does, keeps an encrypted copy of the
conversations you choose, and answers commands you define.

```mermaid
flowchart LR
  Phone[Your phone<br/>Google Messages] <--> Google[Google Messages relay]
  Google <--> T[transport<br/>libgm adapter]
  subgraph Service[SmsBot service - one Go process]
    T --> B[bot engine<br/>commands, plans, sends]
    T --> C[collection<br/>consent, capture, gap repair]
    C --> H[history + archive workers<br/>sync, originals, export]
    B -. consent .-> C
    B <--> S[(store<br/>encrypted SQLite)]
    C <--> S
    H <--> S
    A[httpapi<br/>owner API v1] --> B
    A --> C
    A --> S
    W[web dashboard<br/>embedded assets] --- A
  end
  Browser[Browser or installed phone app] <--> W
  B -. optional .-> P[AI provider<br/>OpenRouter or compatible endpoint]
```

## Packages

| Package | Responsibility |
|---|---|
| `cmd/smsbot` | Command line: serve, initialize, pair, update cookies, backup and restore. |
| `internal/app` | Wires the services together, runs startup in order (recover and pause before connecting), and owns every background worker. |
| `internal/transport` | Conversation and message types, the Google adapter (libgm) and the offline fixture used by the demo and tests. |
| `internal/store` | SQLite with migrations and encrypted payloads: jobs, messages, enrollment, AI threads, credentials, backups. |
| `internal/vault` | The master key and AES-GCM encryption. Windows uses DPAPI; Linux uses an owner-only key file. |
| `internal/bot` | Command routing, AI generation, and the dispatcher that sends replies and your own messages. |
| `internal/collection` | Which conversations are archived, capturing their messages, and read receipts. It never sends. |
| `internal/history` | Background reads that fill the archive. History never runs commands. |
| `internal/archive` | Original files, storage statistics, exports and restore. |
| `internal/notify` | Web Push notifications for each device. |
| `internal/media` | Attachment validation and limits. |
| `internal/httpapi` | The owner-only JSON API ([OpenAPI](../api/openapi.yaml)), sessions, and the embedded dashboard. |
| `web/` | The React and TypeScript dashboard; see [web/README.md](../web/README.md). |

## How a reply is sent

1. A fresh text message arrives from an enrolled person in an individual conversation,
   or from an enrolled member of a group whose people are as the owner approved.
2. If it's a command, the bot stores an encrypted, unchangeable plan and a job.
3. Just before sending, the checks run again: enrollment, pause, freshness and quiet hours.
4. The outgoing message is recorded before it's handed to Google. The job then becomes
   `submitted`, or `unknown_submission` if the result can't be known.

Messages you type in the dashboard follow the same steps, keyed so that a retry never
sends twice.

## Safety rules

- **Exact participants.** In an individual conversation the bot acts only for the enrolled
  person, matched by participant ID. A group can be read and archived, under a fixed identity of its own
  (`group:` and its conversation ID) so membership changes don't interrupt it; its
  `roster` names exactly who is in it. The owner can send to a group, but only as the
  roster they were shown: the request, the queued plan, the pre-send check and the
  transport's own lookup all compare it, and a change cancels the send. The bot runs
  in a group only for members the owner enrolled (`group_enrollment`: conversation,
  member, approved roster) and only while the live roster is the approved one; a change
  silences it until the owner approves again, and each member has their own AI memory
  and threads. The owner's own messages never run commands in a group.
- **Fresh input only.** History and outgoing echoes never run commands. A message
  runs one only if it is a command or a reply to an AI answer; a file it carries
  goes to the AI. A file alone, not replying to an AI answer, never runs one.
- **Record before sending, never retry the uncertain.** An unknown outcome is shown to
  you and is never retried automatically.
- **Pause wins.** Every start is paused, and pausing cancels queued work.
- **Plans don't change.** Editing a command doesn't affect requests already queued.
- **No secrets in logs.** Message text, contacts, credentials and raw errors from Google
  are never logged.

## Where things live

- **Sending:** `bot.Engine.Dispatch` runs each job's steps under one lock and releases it
  only while waiting on the AI provider or uploading files.
- **AI threads:** each `!ask` answer is linked to its upstream message ID, so a native
  reply to it continues that thread. Only the replied-to branch is sent as context. A
  message quoted with an AI command brings the chain of replies above it into the
  prompt, each hop resolved within the same conversation. A file it quotes or carries is
  planned by reference and read only while generating, through the same access checks as
  the dashboard. `internal/bot/attachments.go` decides how each kind is read: photos are
  shrunk, text and office documents (`documents.go`, standard-library zip and XML with
  a cap on what is unpacked) become prompt text for any model, and PDFs, audio and video
  go to the provider as they are. The model's catalog modalities are not used to refuse a
  file; a provider that rejects it (HTTP 4xx) is reported in the chat.
- **Archive sync:** each workflow (import, recent check, viewed section) keeps its own
  checkpoint, and `internal/history/plan.go` decides what runs next.
- **Database access:** queries live in `internal/store` as named operations.
- **Dashboard URLs:** each page and open conversation has its own path
  (`/settings`, `/conversations/ID`), defined in `web/src/lib/routes.ts`, so Back,
  reload and bookmarks work. The server answers those paths with the app
  (`internal/httpapi/dashboard.go`); other unknown paths stay 404.
- **Dashboard state:** `useDashboard` owns app-wide polling; `useConversationMessages`,
  `useThreadScroll` and `useComposer` own a single conversation.
- **State on disk:** the data directory holds `smsbot.db`, the encrypted Google session
  and archived files; the key lives elsewhere. A lock stops two processes from using
  the same directory.
