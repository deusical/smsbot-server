# Getting started

## Requirements

- Go 1.27.3 and Node.js 24.15 or newer in the 24.x series (dependencies are locked).
- For a real account: a phone with Google Messages and a desktop browser to pair it.
- Optional: an OpenRouter key or an OpenAI-compatible endpoint for AI commands.

## Build

**Windows (PowerShell)**

```powershell
.\scripts\build.ps1                          # npm ci, dashboard build, frontend/Go tests and vet, binary
.\scripts\build.ps1 -SkipInstall -SkipChecks # quick rebuild with unchanged dependencies
```

**Linux or macOS**

```sh
(cd web && npm ci && npm run build)
node scripts/sync-web-assets.mjs     # embed the dashboard into the binary
go build -trimpath -ldflags="-s -w" -o bin/smsbot ./cmd/smsbot
```

Or build a container image: `docker build -t smsbot .` (see [Hosting](HOSTING.md)).

## Try the offline demo

```powershell
.\scripts\start.ps1
```

This runs the **fixture** transport: synthetic conversations, no Google connection and
no AI calls. Open <http://127.0.0.1:8787>, read the owner credential from the file the
launcher names, and paste it into the sign-in page. Never paste that credential
anywhere else.

In **Conversations** turn on **Bot replies** for the demo conversation, flip the top-bar
switch to **Live**, open the conversation and use **Simulate !ping**. Activity shows the
reply as submitted. The service keeps running with the browser closed, and every start
begins **paused**.

## Pair a real Google Messages account

Google session cookies give broad access to the account. Use your own account, do the
steps locally, and keep the cookies out of chat tools, issue trackers and screenshots.
This follows the [upstream pairing guide](https://docs.mau.fi/bridges/go/gmessages/authentication.html).

1. Stop any other bot or service that handles the conversation you will test.
2. In a **private browser window**, pair "Messages for web" with your phone, then copy
   the `/web/config` request as **cURL (bash)** from the browser's network tools.
3. Run the pairing command locally. The cookies are read from your clipboard through
   stdin; they are never placed on a command line:

   ```powershell
   .\scripts\start.ps1 -Transport Google -Pair
   ```

4. Approve the emoji shown in the terminal on your phone. Then clear your clipboard.
5. Start the service: `.\scripts\start.ps1 -Transport Google`. It starts paused.
6. In the dashboard, enable **Bot replies** (and, optionally, collection under
   **Settings > Messages & privacy**) for **one designated test conversation** and try it
   before widening anything.

Google Messages chooses SMS, RCS and fallback; the server does not force RCS. The phone
must stay online. The first conversation picker shows the first 25 inbox threads.

On Linux the same steps use the binary directly:

```sh
smsbot --data=STATE_DIR --key=KEY_PATH --init --owner-file=OWNER_FILE
xclip -o | smsbot --data=STATE_DIR --key=KEY_PATH --pair --cookies-stdin   # macOS: pbpaste
smsbot --data=STATE_DIR --key=KEY_PATH
```

## Update cookies without pairing again

If Google starts rejecting the saved credentials (HTTP 401 in the diagnostics), stop the
service, copy a fresh `/web/config` request from the same private browser session and
Google account, and run:

```powershell
.\scripts\start.ps1 -Transport Google -UpdateCookies
```

This offline command takes the state lock, saves an encrypted backup beside the session
(`.before-cookie-update-*.enc`), and replaces only the imported cookies, keeping the
pairing keys and identities. Clear the clipboard, close the private window without
signing out, and restart the service, leaving automation paused while you check it
reconnects. Do not use `-Pair` to overwrite an existing pairing.

## Use it from your phone

Run the service on an always-on host and open it through a private HTTPS address; the
dashboard installs as an app. See [Hosting](HOSTING.md#tailscale-on-an-always-on-host).

## Troubleshooting

| Symptom | What to check |
|---|---|
| `invalid host` in the browser | `--origin` must match the exact name and port in the address bar. |
| Phone shows "not connected" | The phone must be online with Google Messages open or allowed to run in the background. The dashboard reconnects automatically; sending stays blocked until the phone answers. |
| Repeated 401 in the authentication diagnostics | Update the cookies as above. |
| `state directory is already in use` | Another service or offline command holds the lock; stop it first. |
| Sending is paused after a restart | Expected. Every start is paused; use the top-bar switch when ready. |
