# Fase 3 — Multi-source: scanlation aggregators y groups

Living doc. Cuando cierre, destilamos a ADR 0014 y lo archivamos.

## El problema

MangaDex como fuente única deja gaps reales:

1. **Mangas con baja cobertura en MD pero alta en otros sites.** Ejemplo: "The Knight Only Lives Once" tiene 1 capítulo en MD; Asura Scans publica decenas. La traducción vive afuera y nunca sube.
2. **Scanlations pre-oficiales.** Mangas con licencia (One Piece, Jujutsu Kaisen, etc.) tienen cadencia oficial via Shueisha/MangaPlus, pero scanlation groups sacan no-oficiales horas/días antes. Esas versiones no entran a MD por respeto al licensing.
3. **Manga ongoing en source nativo de la traducción.** Manhwa coreano/manhua chino tienen ecosistemas paralelos (Asura, Reaper, Webtoon traducciones de fans) donde el contenido vive primero o únicamente.

Síntoma percibido: "mokuji dice que mi manga no tiene caps nuevos, pero en X site sí los hay desde hace una semana".

## No objetivos

- **Aggregator universal**: no apuntamos a indexar todo internet. Apuntamos a ~3 sources que cubran 90% del catálogo realista del user.
- **DRM-free / hosting propio**: NO hosteamos los caps en mokuji. Seguimos siendo un tracker — el lector siempre es externo.
- **Sources oficiales de pago**: Crunchyroll Manga, MangaPlus subscription, etc. fuera de scope.
- **Reemplazo de MangaDex**: MD sigue siendo la fuente principal cuando tiene los datos. Los demás son complemento.

## Catálogo inicial de fuentes candidatas

| Source | Tipo | API | Coverage | Estado |
|--------|------|-----|----------|--------|
| MangaDex | Aggregator | JSON pública (estable) | Amplia, oficial-friendly | **YA integrado** |
| Asura Scans | Scanlation group propio | Solo HTML (cambia de dominio cada tanto) | Manhwa popular, manhwa de aventura | Candidato fuerte (mencionado por user) |
| Reaper Scans | Scanlation group propio | Solo HTML | Manhwa/manhua, overlap parcial con Asura | Candidato |
| Bato.to | Aggregator | Solo HTML | Amplia, incluye lo que MD rechaza | Candidato |
| Comick.app | Aggregator | JSON pública (no documentada) | Amplia, agrupa scanlation groups | Candidato fuerte |
| MangaPlus | Oficial Shueisha | JSON pública | Catálogo Shueisha | YA via MD externalURL |
| WeebCentral / WCO / Mangakakalot | Spam aggregators | HTML | Variable, baja calidad | Descartar |
| RSS feeds de scanlators | Por grupo | RSS/Atom | Alta calidad pero setup manual | Postergar |

**Por discutir.** ¿Cuáles entran a v1? Mi sospecha: Comick (API + cobertura amplia) + Asura (caso de user real) son los dos primeros. Bato.to en v2.

## Dimensiones de diseño

Voy a iterar con el user dimensión por dimensión, igual que F2. Cuando cierre cada una, lo anoto acá.

1. **Set de fuentes para v1.** ¿Cuáles soportamos primero? Costo: cada source = parser/cliente + tests + tracking de cambios de site (los que scrapeamos).
2. **Identificación cross-source de manga.** Cuando agregas "Solo Leveling" en mokuji, ¿cómo sabemos que el "Solo Leveling" de Asura es el mismo de MD? ¿Matching automático por título normalizado? ¿O el user elige por source manualmente al agregar?
3. **Modelo de datos: manga ↔ sources.** Hoy `manga` tiene `mangadex_id`. ¿Migrar a tabla `manga_sources(manga_id, source, source_id, status)` que soporte N? ¿Mantener `mangadex_id` como caso especial?
4. **Chapter dedup.** Si Asura publica cap 100 y MD también, ¿son el mismo cap? ¿Mostramos 2x con label de source? ¿Consolidamos por número y mostramos el más reciente? ¿Por idioma?
5. **Worker complexity.** Hoy un worker (1h tick) polleea MD. Ahora N sources × M mangas. Rate limiting cross-source (Comick API tiene su propio quota, Asura tiene anti-scraping).
6. **Manual entry escape valve.** Permitir al user agregar a mano un cap con `URL + número + título + fecha`, cuando ninguna source detecta. ¿En v1 o postergar?
7. **Compliance / self-hosters.** Asura/Comick están en zona gris legal. ¿Toggle por self-hoster para deshabilitar scraping sources? ¿O cada source es opcional via env var?
8. **UX del user.** ¿El user elige sources globalmente? ¿Por manga? ¿Default automático?
9. **Anti-scraping management.** Asura cambia de dominio cada tanto. Cuando el parser se rompe (HTML cambia), ¿cómo lo detectamos antes de que el user note? Health checks, dashboard, alertas.
10. **Cómo migra lo que ya está.** Los mangas ya en biblioteca con `mangadex_id` poblado — ¿se les pregunta al user si quiere agregar Asura? ¿auto-search en background?

---

## Decisiones cerradas

(vacío — agregar a medida que cerremos cada dimensión)

## Plan de implementación

(redactar cuando todas las dimensiones cierren)
