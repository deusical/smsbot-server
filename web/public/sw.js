// App shell cache so the installed app opens instantly and offline.
// API responses are never cached: they carry message text and live state.
const SHELL = "smsbot-shell-v1";
const STATIC = ["/manifest.webmanifest", "/icons/icon.svg", "/icons/icon-192.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(SHELL)
      .then((c) => c.addAll(STATIC))
      .then(() => fetch("/", { cache: "no-cache" }))
      .then((response) => storeShell(response)),
  );
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// Cache a page together with the hashed bundles it references, then drop
// bundles it no longer needs. The cached page only changes once all of its
// bundles are stored, so an interrupted update keeps the previous working shell.
async function storeShell(response) {
  if (!response.ok) return;
  const html = await response.clone().text();
  const assets = [...new Set(html.match(/\/assets\/[^"'\s)]+/g) ?? [])];
  const cache = await caches.open(SHELL);
  const missing = [];
  for (const a of assets) if (!(await cache.match(a))) missing.push(a);
  await cache.addAll(missing);
  await cache.put("/", response);
  const keep = new Set(assets);
  for (const request of await cache.keys()) {
    const path = new URL(request.url).pathname;
    if (path.startsWith("/assets/") && !keep.has(path)) await cache.delete(request);
  }
}

// Server errors (such as a proxy 502 while the service restarts) fall back to
// the cached copy, like a lost connection.
async function networkFirst(event, key) {
  const cache = await caches.open(SHELL);
  let response;
  try {
    response = await fetch(event.request);
  } catch (e) {
    const cached = await cache.match(key);
    if (cached) return cached;
    throw e;
  }
  if (!response.ok) return (await cache.match(key)) ?? response;
  if (key === "/") event.waitUntil(storeShell(response.clone()).catch(() => {}));
  else await cache.put(key, response.clone());
  return response;
}

async function cacheFirst(request) {
  const cache = await caches.open(SHELL);
  const cached = await cache.match(request);
  if (cached) return cached;
  const response = await fetch(request);
  if (response.ok) await cache.put(request, response.clone());
  return response;
}

self.addEventListener("fetch", (event) => {
  const { request } = event;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname === "/healthz") return;
  if (request.mode === "navigate") event.respondWith(networkFirst(event, "/"));
  else if (url.pathname.startsWith("/assets/")) event.respondWith(cacheFirst(request));
  else event.respondWith(networkFirst(event, request));
});
