// Registro del service worker. Se carga con `defer` desde layout.html, así
// que se ejecuta después del DOM parsed.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}
