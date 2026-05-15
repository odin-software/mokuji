# ADR 0012: CI/CD con Forgejo runner host-based y script con sudoers

- **Estado**: Aceptada
- **Fecha**: 2026-05-15

## Contexto

El MVP de mokuji está deployado en mimir y, hasta este punto, los updates se hacían manualmente: `make build-linux-amd64`, `scp` del binario a `/tmp/mokuji`, `ssh` a mimir, `sudo install`, `sudo systemctl restart mokuji`, mirar `journalctl`. Funciona, pero tiene tres problemas:

1. **Fricción**: cuatro comandos en dos máquinas para cada cambio chico (typo, bump de `VERSION`, fix de CSS) desincentiva commits pequeños.
2. **Sin verificación post-deploy**: el flow manual no chequea `/health`. Si el binario nuevo crashea al arranque, no nos enteramos hasta visitar la página.
3. **Sin rollback**: el binario viejo se sobrescribe. Si el nuevo falla, no hay vuelta atrás trivial.

Queremos CD automatizado: push a `main` deploya solo, con health check y rollback. Manteniendo la filosofía self-hosting de Odin (sin depender de servicios externos como GitHub Actions, Vercel, etc.) y coherente con la postura de privilegio mínimo del resto del stack.

Forgejo ya corre self-hosted en `git.odin.do` (VPS odin). Forgejo soporta runners host-based (binario que se registra contra una instancia y ejecuta workflows).

## Decisión

CD en tres piezas:

1. **Workflow del repo**: [`.forgejo/workflows/deploy.yml`](../../.forgejo/workflows/deploy.yml). Dispara en `push` a `main`. Hace checkout, `make build-linux-amd64`, y llama al script de deploy con `sudo`. Trivial:

   ```yaml
   jobs:
     deploy:
       runs-on: host
       steps:
         - uses: actions/checkout@v4
         - run: make build-linux-amd64
         - run: sudo /usr/local/bin/mokuji-deploy "$(pwd)/bin/mokuji-linux-amd64"
   ```

2. **Forgejo runner en mimir**: binario `forgejo-runner` corriendo como usuario `runner` (sin privilegios), registrado contra `git.odin.do`. `runs-on: host` ejecuta los steps directo en el filesystem del host (no en container). Esto es deliberado: el script de deploy necesita acceso al systemd local.

3. **Script `mokuji-deploy` en mimir** (vive **fuera del repo**, en `/usr/local/bin/mokuji-deploy`, owned por `root:root`, modo `0755`):

   ```bash
   #!/bin/bash
   set -euo pipefail

   NEW_BINARY="${1:?usage: mokuji-deploy <path-to-new-binary>}"

   if [ ! -f "$NEW_BINARY" ]; then
       echo "Error: binary not found at $NEW_BINARY" >&2
       exit 1
   fi

   if ! file "$NEW_BINARY" | grep -q "ELF.*x86-64"; then
       echo "Error: file is not a Linux x86-64 ELF binary" >&2
       exit 1
   fi

   if [ -f /usr/local/bin/mokuji ]; then
       cp /usr/local/bin/mokuji /usr/local/bin/mokuji.prev
   fi

   install -m 0755 -o root -g root "$NEW_BINARY" /usr/local/bin/mokuji.new
   mv /usr/local/bin/mokuji.new /usr/local/bin/mokuji

   systemctl restart mokuji
   sleep 3

   if ! curl -sf -m 10 http://127.0.0.1:8080/health > /dev/null; then
       echo "Error: health check failed after restart" >&2
       echo "Previous binary preserved at /usr/local/bin/mokuji.prev for manual rollback" >&2
       exit 1
   fi

   echo "Deploy successful"
   ```

   El script valida el input (existe, es ELF x86-64), preserva el binario anterior como `mokuji.prev`, hace install atómico (`install` + `mv`), reinicia el servicio, espera 3s, y verifica `/health`. Si el health check falla, aborta con código de salida no-cero (el workflow lo marca como fallido en Forgejo) y deja `mokuji.prev` listo para rollback manual.

4. **Sudoers** (`/etc/sudoers.d/mokuji-deploy`):

   ```
   # Forgejo Actions runner: deploy mokuji con NOPASSWD
   runner ALL=(root) NOPASSWD: /usr/local/bin/mokuji-deploy *
   ```

   El usuario `runner` solo puede invocar **ese script específico** vía sudo. Cualquier otra cosa requiere password (que el runner no tiene).

## Razones

1. **Mismo VPS, sin secretos a sincronizar**. El runner corre en mimir; el binario se buildea y se instala en el mismo host. No hay que pasar SSH keys, env vars, ni nada entre máquinas. Forgejo runner se autentica contra Forgejo con un token registrado una sola vez al inicio.
2. **Coherente con filosofía self-hosting**. Forgejo ya es nuestra plataforma de código; Forgejo runner es la extensión natural. Sin acoplar el proyecto a GitHub Actions, GitLab CI, Drone, etc.
3. **Privilegio mínimo**. El runner es un usuario sin privilegios. Solo puede invocar `mokuji-deploy` vía sudo, no es root general. Si un workflow malicioso (PR con cambios al workflow) intentara `sudo systemctl stop forgejo` o `sudo cat /etc/shadow`, falla por sudoers.
4. **El script vive fuera del repo, deliberadamente**. Si viviera en el repo y el workflow lo ejecutara directo, cualquier PR podría modificar el script con privilegios root al mergearse. Mantenerlo en `/usr/local/bin/` significa que cambiarlo requiere SSH y root explícito. Trade-off: bumps al script son operativos (no versionados con el código), pero el script casi nunca cambia.
5. **Rollback automático mínimo**. El script preserva el binario anterior como `mokuji.prev`. Si el health check falla, el workflow queda en rojo y un humano puede `mv /usr/local/bin/mokuji.prev /usr/local/bin/mokuji && systemctl restart mokuji`. No es zero-downtime ni automático del todo, pero cubre el 90% de los casos (binario nuevo crashea al arranque o no escucha).
6. **Health check post-restart**. Endpoint `/health` ya existía (ADR previo de server) y devuelve 200 con `{"status":"ok"}`. El script espera 3s y hace `curl -sf -m 10` — sale no-cero si el server no responde, el workflow detecta el fallo.

## Alternativas consideradas

- **Runner en odin, SSH a mimir**: requiere generar key dedicada, distribuirla, pinearla en `authorized_keys` de mimir, y guardar la private key como secret en Forgejo. Más superficie de ataque (otra credencial que rotar). El runner host-based en mimir lo evita por completo.
- **GitHub Actions con SSH a mimir**: idem al anterior pero peor — depende de un servicio externo, contradice la filosofía Odin de no acoplar el stack a empresas terceras.
- **Webhook HTTP a un endpoint en mimir** (estilo "push-to-deploy" sin runner): hay que exponer un endpoint nuevo, autenticarlo, y mantenerlo. El runner cubre lo mismo con menos código y autenticación reutilizada de Forgejo.
- **GitOps con Argo / Flux**: pensados para Kubernetes / múltiples clusters. Sobreingeniería absurda para un binario único en un VPS.
- **Runner sin sudoers, corriendo como root**: el script no necesitaría sudo, pero el runner tendría root general. Cualquier vulnerabilidad en Forgejo runner o en una action de terceros se traduce en root del host. Inaceptable.
- **Script con `--no-validate` o variantes**: descartado, el costo de las 2-3 validaciones es nulo y la sorpresa de un binario corrupto / arquitectura equivocada compensa con creces.

## Consecuencias

**Más fácil:**
- Bump de versión + push deploya en ~1 minuto sin intervención.
- Cambios chicos (typo, CSS, copy) bajan la fricción a "git push".
- El estado del último deploy queda visible en Forgejo Actions (rojo si falló).
- `curl https://mokuji.org/version` confirma qué versión corre prod (junto con [archivo VERSION](../../VERSION) y [Makefile](../../Makefile) que la inyectan).

**Más difícil:**
- Cambios al script `mokuji-deploy` no están versionados con el código (vive en mimir). Si el script crece, considerar moverlo al repo con un mecanismo de "instalar el script en mimir" controlado (Ansible / `make deploy-script` con confirmación). Por ahora es pequeño y cambia rara vez.
- Rollback no es automático — si el health check falla, hay que SSH a mimir y `mv mokuji.prev mokuji`. Aceptable mientras no haya pager 24/7.
- Push directo a main = deploy directo. No hay staging. Para mokuji.org (proyecto personal sin SLA) es aceptable; si en el futuro tuviéramos usuarios sensibles a downtime, agregar branch `staging` con su propio workflow + entorno.

**Para self-hosters** (parte del contrato "self-hostable estilo Miniflux" del [ADR 0001](0001-modelo-multitenant-open-source.md)):
1. Registrar un Forgejo runner host-based en su servidor.
2. Crear `/usr/local/bin/mokuji-deploy` con el contenido de arriba.
3. Crear `/etc/sudoers.d/mokuji-deploy` con la línea NOPASSWD para su usuario del runner.
4. El workflow del repo funciona tal cual.

## Regla de evolución

Revisitar cuando:
- Tengamos múltiples instancias / múltiples nodos. Este flow asume un único host. Si crecemos a varios mokujis (replicación, geo-distribución), reemplazar por un mecanismo orientado a flota.
- Necesitemos zero-downtime real. Hoy hay una ventana corta (segundos) durante `systemctl restart`. Para eliminarla habría que migrar a `Type=notify` con `sd_notify` + socket activation o blue-green con dos units. Anotado en [decisiones-pendientes.md](../decisiones-pendientes.md).
- El script crezca lo suficiente para justificar versionarlo. Patrón candidato: `scripts/mokuji-deploy.sh` en el repo + paso de install controlado.
- Aparezca necesidad real de staging. No antes.
