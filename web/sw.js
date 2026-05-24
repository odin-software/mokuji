// Service worker mínimo para que mokuji sea instalable como PWA.
//
// El fetch handler NO intercepta requests cross-origin: las covers de
// manga vienen de CDNs externas (AniList) y deben llegar al browser sin
// pasar por el SW. Cuando interceptábamos todo con fetch(event.request),
// el browser perdía el path normal de <img> cross-origin —
// referrerpolicy="no-referrer" no se propagaba consistentemente al
// fetch interno del SW, especialmente en iOS Safari, y las imágenes
// fallaban (en desktop solo aparecían tras hard reload).
//
// Para same-origin sí respondemos con fetch(event.request) (no-op real
// que satisface el criterio "tiene fetch handler" requerido por iOS
// para considerar la app PWA-instalable).

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin) {
    return;
  }
  event.respondWith(fetch(event.request));
});
