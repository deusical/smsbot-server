# Private hosting and phone access

The default listener is `127.0.0.1:8787` over HTTP for local development. The installable phone app requires HTTPS. A remote phone needs a trusted HTTPS certificate and network access to the host; it cannot connect to the host's loopback address directly.

For private access, run the service on loopback and put an authenticated private HTTPS proxy in front of it. Set `--origin=https://YOUR_PRIVATE_HOST` to the exact browser URL origin, including a nondefault port if used. The proxy must preserve that exact Host header when forwarding to `127.0.0.1:8787`. Browser mutations must send the same Origin. The service ignores Forwarded/X-Forwarded-* headers and never uses them to decide host, origin or authentication. Keep the backend loopback-only and limit the proxy's users to the owner’s intended devices. The owner login remains required.

## Tailscale on an always-on host

[Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve) is the recommended private proxy: it gives the host a trusted HTTPS name inside your tailnet, so phones on the tailnet can reach the dashboard and install it as an app. Nothing is published to the internet.

1. In the Tailscale admin console, enable MagicDNS and HTTPS certificates. Note the host's name, for example `YOUR_HOST.YOUR_TAILNET.ts.net`.
2. Start the service on loopback with that exact origin:

   ```sh
   smsbot --data=/srv/smsbot/state --key=/srv/smsbot-vault/master.key      --listen=127.0.0.1:8787 --origin=https://YOUR_HOST.YOUR_TAILNET.ts.net
   ```

3. Share it inside the tailnet: `tailscale serve --bg 8787`. Do not use `tailscale funnel`, which makes it public.
4. On the phone (signed in to Tailscale), open `https://YOUR_HOST.YOUR_TAILNET.ts.net` and sign in with the owner credential. Turn on **Keep me signed in** for a personal phone. If the phone is lost, use **Sign out everywhere** in Settings > Devices (and remove it from your tailnet).
5. Install it: in Chrome on Android, open the menu and choose **Install app** (or **Add to Home screen**). In Safari on iPhone, tap Share, then **Add to Home Screen**.

Serve preserves the Host header, which the service checks exactly; if the dashboard answers `invalid host`, the `--origin` does not match the name in the browser. Use tailnet access controls to limit who can reach the host. The owner login remains required.

The installed app caches only the dashboard's static files so it opens instantly and offline. API responses, which carry message text, are never cached. Signing out revokes the session on the server.

## Direct TLS

Supply both `--tls-cert` and `--tls-key`. A nonloopback listener requires direct TLS and an explicit HTTPS origin. Certificates must cover the origin hostname and chain to a CA trusted by the phone/browser. Protect the private key and allow only intended private-network sources in the host firewall.

```sh
smsbot --data=/srv/smsbot/state --key=/srv/smsbot-vault/master.key \
  --listen=0.0.0.0:8787 --origin=https://YOUR_PRIVATE_HOST:8787 \
  --tls-cert=/srv/smsbot-tls/fullchain.pem --tls-key=/srv/smsbot-tls/privkey.pem
```

Use a dedicated nonroot service identity. Keep state and the vault key in separate private directories on local storage, with owner-only access. Initialize once with `--init --owner-file=/srv/smsbot-vault/owner.txt` and the same data/key arguments; read the credential locally. Never put credentials in arguments, environment variables, compose files, source, screenshots or logs. Pairing remains a local owner action with phone emoji approval; no deployment process should import an existing Windows vault into Linux automatically.

The Dockerfile uses [multi-stage builds](https://docs.docker.com/build/building/multi-stage/) to compile the dashboard and Go service, then runs as UID/GID 10001 with CA certificates. `compose.example.yaml` is a direct-TLS template with separate named state/vault volumes, a read-only root filesystem and a read-only TLS mount. Its published port binds the host's loopback by default; changing that bind is an explicit private-network deployment decision. Set the example's host and certificate directory locally. Ensure UID 10001 can read TLS files. The sample runs Google mode only after owner-controlled initialization and pairing; add `--fixture` for offline rehearsal.

Build locally with `docker build -t smsbot-local .`. For a disposable fixture rehearsal, initialize new named volumes using `docker run --rm` with `--init`, then run the image with the same volumes and `--fixture`. To reach HTTP from outside a container without TLS, a shared host network and loopback listener are required on a Linux host; a bridged container's nonloopback listener intentionally requires TLS. Do not copy production state into a test container.

Every startup pauses automation. Stop gracefully with Ctrl+C, SIGTERM or `docker stop`; wait for the service to exit before backups, cookie updates or restarts. Configure restart supervision deliberately: a restart restores connectivity but does not reenable replies. Review activity and uncertainty before reenabling. No uncertain outbound submission is automatically retried.

Final personal-phone acceptance requires the owner to configure the private network/certificate, open the exact HTTPS origin on the phone, install and sign in to the dashboard, and verify readiness and a designated conversation. Those device, certificate, and live transport checks are separate from synthetic container validation and remain pending until actually performed.
