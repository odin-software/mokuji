// Service worker mínimo para que mokuji sea instalable como PWA.
// Por ahora es pass-through (network-first sin caching). Estrategias de
// offline se agregarán cuando definamos qué assets cachear con qué política.

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  event.respondWith(fetch(event.request));
});
