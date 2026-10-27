# Getting started

## Requirements

- Go 1.27.3 and Node.js 24.15 or newer in the 24.x series.
- For a real account: an Android phone with Google Messages and a desktop browser to
  pair it.
- Optional: an OpenRouter key or any OpenAI-compatible endpoint for AI commands.

## Build

On Windows:

```powershell
.\scripts\build.ps1                          # install, build, run the checks
.\scripts\build.ps1 -SkipInstall -SkipChecks # quick rebuild
```

On Linux or macOS:

```sh
(cd web && npm ci && npm run build)
node scripts/sync-web-assets.mjs     # embed the dashboard in the binary
go build -trimpath -ldflags="-s -w" -o bin/smsbot ./cmd/smsbot
```

Or build a container: `docker build -t smsbot .`

## Try the offline demo

```powershell
.\scripts\start.ps1
```

The demo uses synthetic conversations and never contacts Google or an AI provider.
Open <http://127.0.0.1:8787>, read the owner credential from the file the launcher
names, and paste it into the sign-in page. Turn on **Bot replies** for a conversation,
switch the top bar to **Live**, open the conversation and press **Simulate !ping**.

Every start is **paused**: nothing is sent until you switch to Live.

## Pair your Google Messages account

Pairing imports Google session cookies, which give broad access to your account. Do it
on your own machine and keep the cookies out of chat tools, issues and screenshots.
This follows the [mautrix pairing guide](https://docs.mau.fi/bridges/go/gmessages/authentication.html).

1. In a **private browser window**, pair "Messages for web" with your phone. In the
   browser's network tools, copy the `/web/config` request as **cURL (bash)**.
2. Run the pairing command. It reads the copied request from your clipboard:

   ```powershell
   .\scripts\start.ps1 -Transport Google -Pair
   ```

3. Approve the emoji shown in the terminal on your phone, then clear your clipboard.
4. Start the service with `.\scripts\start.ps1 -Transport Google`.
5. Turn on **Bot replies** for one test conversation and try it before enabling more.

On Linux, use the binary directly:

```sh
smsbot --data=STATE_DIR --key=KEY_PATH --init --owner-file=OWNER_FILE
xclip -o | smsbot --data=STATE_DIR --key=KEY_PATH --pair --cookies-stdin   # macOS: pbpaste
smsbot --data=STATE_DIR --key=KEY_PATH
```

### When Google rejects the cookies

If the dashboard reports repeated sign-in failures (HTTP 401), stop the service, copy a
fresh `/web/config` request from the same private browser session, and run:

```powershell
.\scripts\start.ps1 -Transport Google -UpdateCookies
```

On Linux: `xclip -o | smsbot --data=STATE_DIR --key=KEY_PATH --update-cookies --cookies-stdin`.

This replaces only the cookies and keeps the pairing, saving a backup of the old
session first. Don't use `-Pair` again on an existing pairing.

## Use it from your phone

The dashboard installs as a phone app, which needs HTTPS. The simplest private setup is
an always-on machine with [Tailscale](https://tailscale.com):

1. In the Tailscale admin console, turn on MagicDNS and HTTPS certificates.
2. Start the service on loopback with the machine's Tailscale name as its origin:

   ```sh
   smsbot --data=STATE_DIR --key=KEY_PATH --listen=127.0.0.1:8787 \
     --origin=https://YOUR_HOST.YOUR_TAILNET.ts.net
   ```

3. Share it inside your tailnet with `tailscale serve --bg 8787`. Don't use
   `tailscale funnel`, which would make it public.
4. On your phone, open `https://YOUR_HOST.YOUR_TAILNET.ts.net`, sign in, and choose
   **Install app** (Chrome) or **Add to Home Screen** (Safari).

The service checks the `Host` header exactly. If the browser shows `invalid host`,
`--origin` doesn't match the address you opened. Any other HTTPS proxy works the same
way, as long as it keeps the original `Host` header.

### Direct TLS and Docker

To serve HTTPS without a proxy, pass a certificate. A listener that isn't on loopback
requires this:

```sh
smsbot --data=STATE_DIR --key=KEY_PATH --listen=0.0.0.0:8787 \
  --origin=https://YOUR_HOST:8787 --tls-cert=fullchain.pem --tls-key=privkey.pem
```

`compose.example.yaml` runs the container this way, as a non-root user with a
read-only filesystem and separate volumes for state and the key. Initialize and pair
before starting it; add `--fixture` for an offline trial.

### Updating a remote host

`scripts/deploy-remote.ps1` updates a Linux host over SSH from a Windows checkout of
merged `main`:

```powershell
.\scripts\deploy-remote.ps1 -Target USER@YOUR_HOST -OriginHost YOUR_HOST.YOUR_TAILNET.ts.net:PORT
```

The host runs the service as the user unit `smsbot.service`, with the binary at
`~/.local/lib/smsbot/smsbot` and state under `~/.local/share/smsbot`. The script
builds a Linux binary, backs up the database and session on the host, swaps the binary,
and waits for the health check. It rolls back if anything fails. Set up the host once by
hand first, including pairing; the script never reads keys, credentials or messages.

## Backup and restore

Stop the service first. Backups need the **original key**; without it nothing can be
recovered.

```powershell
smsbot --data STATE_DIR --key KEY_PATH --backup NEW_BACKUP_FILE
smsbot --data STATE_DIR --key KEY_PATH --archive-backup NEW_ARCHIVE.sma                    # whole message archive, encrypted
smsbot --data STATE_DIR --key KEY_PATH --archive-backup NEW_EXPORT.tar --archive-plaintext # readable JSON and files
```

Archive backups accept `--archive-conversation ID` and `--archive-since 2026-01-01T00:00:00Z`.
The **Archive** page can create the same exports while the service runs.

To restore, initialize a fresh state directory with the same key, then restore into it:

```powershell
smsbot --data NEW_STATE_DIR --key KEY_PATH --init --owner-file NEW_OWNER_FILE
smsbot --data NEW_STATE_DIR --key KEY_PATH --restore BACKUP_FILE            # or --archive-restore ARCHIVE.sma
```

Backups leave out the Google pairing, provider keys and owner credentials; set those up
again afterwards. On Windows the key is protected for your Windows account, so it
can't be moved to another account or to Linux.

## Troubleshooting

| Symptom | What to check |
|---|---|
| `invalid host` | `--origin` must match the exact name and port in the address bar. |
| Phone not connected | The phone must be online, with Google Messages allowed to run in the background. Sending waits until it answers. |
| Repeated 401 errors | Update the cookies as described above. |
| `state directory is already in use` | Another copy of the service or an offline command is running. Stop it first. |
| Paused after a restart | Expected: every start is paused. Switch to Live when ready. |
