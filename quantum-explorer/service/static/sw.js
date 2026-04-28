const QE_STATIC_CACHE = "qe-static-v1";
const QE_ASSETS = ["/", "/static/index.html", "/static/chart.umd.min.js", "/logo.png", "/static/manifest.webmanifest"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(QE_STATIC_CACHE).then((cache) => cache.addAll(QE_ASSETS)).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== QE_STATIC_CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/")) {
    event.respondWith(fetch(req));
    return;
  }
  event.respondWith(
    caches.match(req).then((cached) => {
      if (cached) return cached;
      return fetch(req).then((resp) => {
        if (!resp || resp.status !== 200 || resp.type !== "basic") return resp;
        const copy = resp.clone();
        caches.open(QE_STATIC_CACHE).then((cache) => cache.put(req, copy));
        return resp;
      });
    })
  );
});
