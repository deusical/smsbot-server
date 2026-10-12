# Private hosting and phone access

The default listener is `127.0.0.1:8787` over HTTP for local development. Android release clients require HTTPS. A remote phone needs a trusted HTTPS certificate and network access to the host; it cannot connect to the host's loopback address directly.

For private access, run the service on loopback and put an authenticated private HTTPS proxy in front of it. Set `--origin=https://YOUR_PRIVATE_HOST` to the exact browser/Android URL origin, including a nondefault port if used. The proxy must preserve that exact Host header when forwarding to `127.0.0.1:8787`. Browser mutations must send the same Origin. The service ignores Forwarded/X-Forwarded-* headers and never uses them to decide host, origin or authentication. Keep the backend loopback-only and limit the proxy's users to the owner’s intended devices. Application login and revocable Android credentials remain required.

One optional private proxy is [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve), which shares a local service within a tailnet and supports HTTPS. Follow its official setup and access-control instructions locally. This project does not configure a tailnet, publish a public tunnel, or deploy a proxy. Verify the proxy preserves Host; a mismatched hostname deliberately receives `invalid_host`.

```sh
smsbot --data=/srv/smsbot/state --key=/srv/smsbot-vault/master.key \
  --listen=127.0.0.1:8787 --origin=https://YOUR_PRIVATE_HOST
```

For direct TLS, supply both `--tls-cert` and `--tls-key`. A nonloopback listener requires direct TLS and an explicit HTTPS origin. Certificates must cover the origin hostname and chain to a CA trusted by the phone/browser. Do not disable Android certificate validation. Protect the private key and allow only intended private-network sources in the host firewall.

```sh
smsbot --data=/srv/smsbot/state --key=/srv/smsbot-vault/master.key \
  --listen=0.0.0.0:8787 --origin=https://YOUR_PRIVATE_HOST:8787 \
  --tls-cert=/srv/smsbot-tls/fullchain.pem --tls-key=/srv/smsbot-tls/privkey.pem
```

Use a dedicated nonroot service identity. Keep state and the vault key in separate private directories on local storage, with owner-only access. Initialize once with `--init --owner-file=/srv/smsbot-vault/owner.txt` and the same data/key arguments; read the credential locally. Never put credentials in arguments, environment variables, compose files, source, screenshots or logs. Pairing remains a local owner action with phone emoji approval; no deployment process should import an existing Windows vault into Linux automatically.

The Dockerfile uses [multi-stage builds](https://docs.docker.com/build/building/multi-stage/) to compile the dashboard and Go service, then runs as UID/GID 10001 with CA certificates. `compose.example.yaml` is a direct-TLS template with separate named state/vault volumes, a read-only root filesystem and a read-only TLS mount. Its published port binds the host's loopback by default; changing that bind is an explicit private-network deployment decision. Set the example's host and certificate directory locally. Ensure UID 10001 can read TLS files. The sample runs Google mode only after owner-controlled initialization and pairing; add `--fixture` for offline rehearsal.

Build locally with `docker build -t smsbot-local .`. For a disposable fixture rehearsal, initialize new named volumes using `docker run --rm` with `--init`, then run the image with the same volumes and `--fixture`. To reach HTTP from outside a container without TLS, a shared host network and loopback listener are required on a Linux host; a bridged container's nonloopback listener intentionally requires TLS. Do not copy production state into a test container.

Every startup pauses automation. Stop gracefully with Ctrl+C, SIGTERM or `docker stop`; wait for the service to exit before backups, cookie updates or restarts. Configure restart supervision deliberately: a restart restores connectivity but does not reenable replies. Review activity and uncertainty before reenabling. No uncertain outbound submission is automatically retried.

Final personal-phone acceptance requires the owner to configure the private network/certificate, create a client credential in Settings, enter the exact HTTPS origin and credential on Android, and verify readiness and a designated conversation. Those device, certificate, and live transport checks are separate from synthetic container validation and remain pending until actually performed.
