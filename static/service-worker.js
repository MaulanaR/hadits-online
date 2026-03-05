const CACHE_NAME = 'hadits-online-v1';
const ASSETS = [
  '/',
  '/static/style.css',
  '/static/script.js',
  '/static/favorites.js',
  '/static/logo.png',
  '/static/patter_ketupat.png'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(ASSETS);
    })
  );
});

self.addEventListener('fetch', (event) => {
  event.respondWith(
    caches.match(event.request).then((response) => {
      return response || fetch(event.request);
    })
  );
});
