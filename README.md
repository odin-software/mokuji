# mokuji

Tracker de manga público, self-hostable, en Go.

[mokuji.org](https://mokuji.org) corre como instancia pública donde cualquiera puede firmarse y trackear su lista de manga. El código está abierto para que otros corran su propia instancia.

## Estado

En diseño. Sin código todavía. Decisiones arquitectónicas en progreso bajo [docs/adr/](docs/adr/).

## Filosofía

- **File over App**: el código y los datos deben sobrevivir cualquier herramienta
- **Texto plano y formatos abiertos** cuando sea posible
- **Self-hosted donde aporta valor**, comercial donde es claramente mejor
- **Mínimo viable**, expandir solo cuando duela un problema real
- **Sin lock-in** de plataforma ni de proveedor
- **Comprensión sobre conveniencia**: las primitivas de seguridad y core se entienden a fondo antes de usarse, idealmente leyéndose en menos de 100 líneas

## Stack (decisiones cerradas)

| Capa | Elección |
|------|----------|
| Lenguaje | Go (última stable, baseline `go 1.22`) |
| HTTP | stdlib `net/http` puro |
| Frontend | `html/template` server-rendered + vanilla JS |
| Base de datos | SQLite, ruta a Litestream para backup near-realtime |
| Auth | Email OTP codes, sin passwords |
| Deploy | VPS Ubuntu LTS, binario + systemd + nginx + certbot Let's Encrypt |
| Distribución | Multi-tenant SaaS + open source self-hostable |
| PWA | Requisito de producto: debe funcionar instalada en móvil |

Razones y alternativas consideradas para cada decisión en [docs/adr/](docs/adr/).

## Documentación

- [docs/adr/](docs/adr/) — Architecture Decision Records, una por decisión arquitectónica
- [docs/decisiones-pendientes.md](docs/decisiones-pendientes.md) — qué falta decidir y en qué orden

## Licencia

[AGPL-3.0](LICENSE). Significa: cualquiera puede usar, modificar y correr mokuji. Si lo corres como servicio público para otros usuarios, debes ofrecer el código fuente (incluyendo tus modificaciones) a esos usuarios.

Para reportar vulnerabilidades de seguridad, ver [SECURITY.md](SECURITY.md).
