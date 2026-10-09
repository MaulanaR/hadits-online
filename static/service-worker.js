// hadits.online service worker (v2)
// - HTML pages: network-first (fresh content), falling back to the cached copy
//   when offline, then to the cached home page, then to a small offline page.
// - /static/*: stale-while-revalidate (URLs are versioned with ?v=<hash>).
// - /api/* and /search: network only. Cross-origin requests (ads, Midtrans,
//   fonts, CDNs) are never intercepted.
// The server fills in ASSET_VERSION and PRECACHE_ASSETS when it serves
// /service-worker.js, so a deploy with new assets also updates the worker.
const CACHE_NAME = 'hadits-online-v2';
const ASSET_VERSION = 'dev'; // replaced by the server
const PRECACHE_ASSETS = ['/static/style.css', '/static/script.js', '/static/favorites.js', '/static/logo.png', '/static/patter_ketupat.png']; // replaced by the server
const MAX_PAGES = 60;

const OFFLINE_HTML = '<!DOCTYPE html><html lang="id"><head><meta charset="utf-8">' +
  '<meta name="viewport" content="width=device-width, initial-scale=1"><title>Offline - hadits.online</title>' +
  '<style>body{font-family:system-ui,sans-serif;background:#064e3b;color:#fff;display:flex;min-height:100vh;' +
  'align-items:center;justify-content:center;margin:0;padding:1rem;text-align:center}a{color:#6ee7b7}</style></head>' +
  '<body><main><h1>Anda sedang offline</h1><p>Halaman ini belum tersimpan di perangkat Anda.</p>' +
  '<p><a href="/favorites">Buka hadits favorit</a> &middot; <a href="/">Coba lagi</a></p></main></body></html>';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then((cache) => cache.addAll(PRECACHE_ASSETS))
      .catch(() => { /* precache is best effort */ })
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter((n) => n !== CACHE_NAME).map((n) => caches.delete(n)));
    // Drop static files from older asset versions.
    const cache = await caches.open(CACHE_NAME);
    const keep = new Set(PRECACHE_ASSETS.map((u) => new URL(u, self.location.origin).href));
    for (const req of await cache.keys()) {
      const url = new URL(req.url);
      if (url.pathname.startsWith('/static/') && url.searchParams.has('v') && !keep.has(url.href)) {
        await cache.delete(req);
      }
    }
    await self.clients.claim();
  })());
});

async function trimPages(cache) {
  const keys = (await cache.keys()).filter((r) => !new URL(r.url).pathname.startsWith('/static/'));
  for (let i = 0; i < keys.length - MAX_PAGES; i++) {
    await cache.delete(keys[i]);
  }
}

async function networkFirst(event) {
  const cache = await caches.open(CACHE_NAME);
  try {
    const res = await fetch(event.request);
    if (res && res.ok && res.type === 'basic') {
      event.waitUntil(cache.put(event.request, res.clone()).then(() => trimPages(cache)));
    }
    return res;
  } catch (err) {
    return (await cache.match(event.request)) ||
      (await cache.match('/')) ||
      new Response(OFFLINE_HTML, { headers: { 'Content-Type': 'text/html; charset=utf-8' } });
  }
}

async function staleWhileRevalidate(event) {
  const cache = await caches.open(CACHE_NAME);
  const cached = await cache.match(event.request);
  const update = fetch(event.request).then((res) => {
    if (res && res.ok) {
      return cache.put(event.request, res.clone()).then(() => res);
    }
    return res;
  });
  if (cached) {
    event.waitUntil(update.catch(() => {}));
    return cached;
  }
  return update;
}

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // ads, Midtrans, fonts, CDNs
  if (url.pathname.startsWith('/api/') || url.pathname === '/search') return; // network only

  if (req.mode === 'navigate') {
    event.respondWith(networkFirst(event));
  } else if (url.pathname.startsWith('/static/')) {
    event.respondWith(staleWhileRevalidate(event));
  }
});
