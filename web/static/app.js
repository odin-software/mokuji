// Registro del service worker. Se carga con `defer` desde layout.html, así
// que se ejecuta después del DOM parsed.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// Forms con data-auto-submit envían cuando un <select> cambia, eliminando
// el botón "filtrar" explícito. El input search sigue submiteando con
// Enter (default del browser). Fallback sin JS: <noscript><button>.
document.querySelectorAll('form[data-auto-submit]').forEach((form) => {
  form.addEventListener('change', (e) => {
    if (e.target.matches('select')) form.submit();
  });
});
