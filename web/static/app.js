// Registro del service worker. Se carga con `defer` desde layout.html, así
// que se ejecuta después del DOM parsed.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// Marca la sección actual en el nav (sidebar y header) con
// aria-current="page": el lector la anuncia y el CSS la subraya. Sin JS el
// nav funciona igual, solo sin la marca.
document.querySelectorAll('.app-nav nav a, .app-sidebar-nav a').forEach((a) => {
  const href = a.getAttribute('href');
  if (location.pathname === href || location.pathname.startsWith(href + '/')) {
    a.setAttribute('aria-current', 'page');
  }
});
