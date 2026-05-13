# ADR 0004: Autenticación con email OTP codes (sin passwords)

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

Multi-tenant ([ADR 0001](0001-modelo-multitenant-open-source.md)) requiere autenticación real. Restricción adicional crítica: **mokuji debe funcionar como PWA instalada en móvil**, lo cual condiciona el flow de auth porque:

- **iOS particiona storage entre Safari y PWA instalada**: cookies, localStorage, sessionStorage, y service worker no se comparten después del "Add to Home Screen" inicial. iOS hace una copia única de cookies al momento de instalar y nunca más.
- **Magic links recibidos por email abren en Safari, no en la PWA**: el usuario click el link, queda autenticado en Safari, pero la PWA instalada sigue viendo sesión vacía. Es un callejón sin salida documentado en múltiples reportes (ej: [Outline #3192](https://github.com/outline/outline/issues/3192)).
- **Android comparte storage entre Chrome y PWA**, pero diseñar para que Android funcione e iOS se rompa no es opción.

Opciones discutidas:

- Password + verificación email + reset por email
- Magic links por email (link único de un solo uso)
- Email OTP codes (código numérico de un solo uso, que el usuario tipea)
- Combinaciones de las anteriores

## Decisión

**Email OTP codes únicamente, sin passwords.**

Flow:

1. Usuario entra email en el form de login/signup (mismo form, mismo endpoint)
2. Servidor genera código aleatorio de 6 dígitos con `crypto/rand`
3. Servidor guarda el código **hasheado** con expiración (10 min) y flag de single-use
4. Email al usuario con el código grande y legible, formato `123 456`
5. Usuario tipea o pega el código en el form de la app
6. Servidor valida (hash compare, no expirado, no usado), marca como usado, crea sesión, setea cookie

Si el email no existe en la DB al momento del paso 2, la cuenta se crea en vuelo (signup y login son el mismo flow). Esto elimina la distinción explícita entre "registro" y "login" desde la perspectiva del usuario.

## Razones

1. **Único modelo que funciona en iOS PWA sin fricción.** No hay handoff entre contextos: el usuario lee el código desde su app de email y lo introduce manualmente en mokuji. Funciona idéntico en Safari desktop, Chrome desktop, Chrome móvil, PWA iOS, PWA Android.
2. **Elimina ~300 líneas de código sensible** vs password-based: cero password hashing, cero password storage, cero flow de reset, cero flow de cambio, cero validación de fortaleza.
3. **Threat model idéntico al de passwords + reset por email.** Si el sistema tiene "olvidé mi password vía email", el email YA es el factor de auth — el password es solo una capa adicional. OTP remueve la capa redundante y deja explícito quién es el factor real.
4. **Sin riesgo de password reuse**: leak de DB no expone hashes que crackear o que comparar contra leaks públicos.
5. **Sin brute force de login**: no hay password contra qué hacer dictionary attack. Rate limit sigue siendo necesario para evitar spam de OTPs a inboxes ajenos, pero el ataque es generar molestia, no obtener cuentas.
6. **Patrón de uso de mokuji lo tolera bien**: tracker de manga no es Twitter, se abre ocasionalmente. Con cookie de sesión larga (90 días sliding window), el round-trip de email ocurre una vez por dispositivo cada ~3 meses.

## Alternativas consideradas

- **Magic links por email**: descartada por incompatibilidad con PWA iOS. Click en email abre Safari, no la PWA instalada. La sesión queda en el contexto equivocado y el usuario queda atrapado.
- **Password + verificación + reset**: descartada por código adicional sin ganancia de seguridad real (mismo threat model que OTP) y por mantener una primitiva (password hashing) que prefiero evitar si no es necesaria.
- **OTP y passwords en paralelo**: descartada por duplicar superficie de ataque y código sin ganancia clara.
- **OAuth con providers externos (Google, GitHub)**: descartada porque introduce dependencia de terceros, complica self-hosting (cada self-hoster necesita registrar su app en cada provider), y va en contra de la filosofía de ownership.
- **Magic link + OTP en el mismo email**: descartada porque entrena al usuario a clickear el link, lo cual en PWA iOS lo manda a Safari y rompe su sesión instalada. Doble path también duplica superficie de bugs en validación.

## Consecuencias

- Necesitamos **transporte de email confiable** desde día 1. Decisión pendiente en [decisiones-pendientes.md](../decisiones-pendientes.md): SMTP propio vs servicio comercial.
- Schema de DB incluye tabla de **códigos OTP pendientes** con al menos: `email` o `user_id`, `code_hash`, `created_at`, `expires_at`, `used_at`.
- UI del input debe usar `inputmode="numeric"` + `autocomplete="one-time-code"` + `pattern="[0-9]{6}"` + `maxlength="6"` para activar teclado numérico y auto-fill desde email/SMS en iOS/Android.
- **Tradeoff aceptado**: OTP tiene fricción de phishing ligeramente más alta que magic links. Un atacante puede montar una página falsa (homograph attack), pedir email, hacer relay al servidor real para que envíe código al inbox legítimo, pedir el código a la víctima, y hacer relay del código de vuelta para capturar la session cookie. Mitigaciones para hoy: email del código incluye IP/user-agent del que lo pidió, mensaje explícito "no compartas este código", expiración corta (10 min), rate limit estricto de `/auth/verify` por IP. Solución estructural a futuro: passkeys.

## Regla de evolución

Agregar **passkeys (WebAuthn)** como segundo método (opt-in, no reemplazo) cuando:

- Aparezcan reportes de phishing exitoso contra usuarios, o
- El producto crezca a un perfil de tráfico donde el threat model lo amerite, o
- Se quiera ofrecer auth phishing-resistant como diferenciación

Passkeys son phishing-resistant por construcción (firma criptográfica origin-bound), pero introducen complejidad significativa: WebAuthn ceremony (registration + authentication), almacenamiento server-side de public keys por dispositivo, recuperación multi-dispositivo, fallback path si el usuario pierde todos sus passkeys. No se priorizan ahora — explicación pendiente en [decisiones-pendientes.md](../decisiones-pendientes.md).
