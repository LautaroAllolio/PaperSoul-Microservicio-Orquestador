# Task List — Refactorización 12-Factor del Orquestador

Sigue la estructura del plan en `tasks/plan.md`. Orden por dependencias (bottom-up),
slices verticales: cada tarea deja el repo compilable y con la suite verde.

**Archivo previo:** el plan de construcción original está en `tasks/plan-build.md` y
`tasks/todo-build.md` (archivados con `git mv`, sin sobrescribir). Sus ítems sin marcar no se
dan por cerrados.

**Comandos base:** `make build` · `make test` · `make race` · `make vet` · `make integration`
· `make lint-openapi` · `make check-fmt` · `make ci`.

**Decisiones del usuario (2026-11-20):** verificación **solo estática** (no levantar ni
modificar los stacks en `~/docker`: Traefik, Consul, MongoDB) y **un commit por tarea**
(8 commits atómicos).

**Regla de no regresión:** ninguna tarea cambia el contrato HTTP, el motor de validación en
memoria, el dedup por checksum, el semáforo, el singleflight ni el reintento solo por dial.

---

## Phase 0: Fundamentos

### Task 1: Alinear toolchain Go y reducir `.gitignore`

**Description:** Bajar la imagen builder de `golang:1.27-bookworm` a `golang:1.26-bookworm`
para igualar la directiva `go 1.26.0` de `go.mod`, y reemplazar la plantilla Python de 229
líneas de `.gitignore` por una lista mínima Go que conserve todos los ignorados vigentes.
Factores 1, 2 y 10.

**Acceptance criteria:**
- [x] `Dockerfile` usa `golang:1.26-bookworm`; multi-stage, distroless non-root, `ARG`s,
      `ENV ORCH_ADDR` y `ENTRYPOINT` quedan intactos.
- [x] `.gitignore` conserva `.env`, `.envrc`, `/bin/`, `/orchestrator`,
      `tests/vegeta-generated/`, `/consul/consul.json` y `/consul/data/`; desaparecen las
      reglas de Python/UV/Poetry/Celery/Spyder.
- [x] `git status --short` no muestra archivos generados nuevos.

**Verification:**
- [x] Tests pass: `make ci` ejecutado por pasos (check-fmt, vet, test, race, integration,
      lint-openapi) porque el comando único superó el timeout del harness; todos verdes.
- [x] Build succeeds: `docker build -t papersoul-orchestrator:local .` (confirmado con
      `golang:1.26-bookworm`).
- [x] Manual check: `git check-ignore -v .env bin/orchestrator tests/vegeta-generated/report.json`
      y `.env.example` NO ignorado.

**Dependencies:** None (paralelizable con Task 2)

**Files likely touched:** `Dockerfile`, `.gitignore`

**Estimated scope:** Small (2 archivos)

### Task 2: Extender `config.Config` con los parámetros hoy hardcodeados

**Description:** Añadir a `config.Load()` los valores literales que hoy dependen del entorno:
`ORCH_PUBLIC_HOST`, `ORCH_TRAEFIK_ENTRYPOINT`, `ORCH_TRAEFIK_ROUTER`,
`ORCH_TRAEFIK_RETRY_ATTEMPTS`, `ORCH_TRAEFIK_RETRY_INTERVAL`,
`ORCH_TRAEFIK_HEALTH_INTERVAL`, `ORCH_TRAEFIK_HEALTH_TIMEOUT`,
`ORCH_CONSUL_CHECK_INTERVAL`, `ORCH_CONSUL_CHECK_TIMEOUT`,
`ORCH_CONSUL_DEREGISTER_AFTER`, `ORCH_DEREGISTER_TIMEOUT`,
`ORCH_READ_HEADER_TIMEOUT`, `ORCH_IDLE_TIMEOUT` y `ORCH_RETRY_AFTER`. Defaults idénticos a los
actuales, validación positiva y `errors.Join`. Factor 3.

**Acceptance criteria:**
- [ ] Las 14 variables se leen con defaults que reproducen exactamente el comportamiento
      actual (host `orquestador.universidad.localhost`, `https`, `orchestrator`, 2, 500ms,
      10s, 3s, 3s, 1s, 1m, 2s, 5s, 60s, 1).
- [ ] Un host vacío, una duración ≤ 0 o un entero ≤ 0 hacen fallar `Load()` con un mensaje
      que nombra la variable (misma forma que `requiredError`).
- [ ] Ningún archivo de producción conserva el literal `universidad.localhost` fuera de
      `config.go` (Compose, `.env.example` y README quedan fuera del escaneo).

**Verification:**
- [ ] Tests pass: `go test ./internal/platform/config/... && make vet`
- [ ] Build succeeds: `make build`
- [ ] Manual check: `ORCH_TRAEFIK_RETRY_ATTEMPTS=0 go run ./cmd/orchestrator` sale con código 1
      y mensaje explícito.

**Dependencies:** None (paralelizable con Task 1)

**Files likely touched:** `internal/platform/config/config.go`,
`internal/platform/config/config_test.go`

**Estimated scope:** Medium (2 archivos, ~14 campos)

### Checkpoint 1: Fundamentos
- [ ] `make ci` y `docker build -t papersoul-orchestrator:local .` verdes
- [ ] `config.Load()` con defaults y con valores inválidos cubierto por tests
- [ ] Revisión rápida del diff de `.gitignore` (nada de lo vigente se perdió)

---

## Phase 1: Borde HTTP y composition root

### Task 3: `Retry-After` del 503 configurable

**Description:** `MaxConcurrency` pasa a recibir el valor de `ORCH_RETRY_AFTER` (default `1`)
en vez de la constante `retryAfterSeconds`, y `NewRouter` se lo pasa desde `config.Config`.
Mantener el orden de middlewares y la exención de `/health`. Factor 3, KISS.

**Acceptance criteria:**
- [ ] El 503 por saturación emite `Retry-After` con el valor configurado; con default sigue
      siendo `1`.
- [ ] `/health` sigue exento del semáforo y todos los errores siguen siendo `problem+json`.
- [ ] Hay tests para el default y para un valor custom (p. ej. `7`).

**Verification:**
- [ ] Tests pass: `go test ./internal/handler/... && make ci`
- [ ] Build succeeds: `make build`
- [ ] Manual check: revisar que `rutas`, `RequestID`, `RequestLogger` y `Recoverer` no cambiaron.

**Dependencies:** Task 2

**Files likely touched:** `internal/handler/middleware.go`,
`internal/handler/middleware_test.go`, `internal/handler/router.go`,
`internal/handler/router_test.go`

**Estimated scope:** Medium (4 archivos)

### Task 4: Timeouts del `http.Server` desde configuración

**Description:** `newServer` usa `cfg.ReadHeaderTimeout` e `cfg.IdleTimeout` en lugar de las
constantes `readHeaderTimeout`/`idleTimeout`. `WriteTimeout` permanece en 0 a propósito.
Factor 3.

**Acceptance criteria:**
- [ ] Con defaults, `ReadHeaderTimeout = 5s` e `IdleTimeout = 60s`; `WriteTimeout = 0`.
- [ ] El log de arranque/cierre sigue expresando duraciones en milisegundos vía `milisegundos()`.
- [ ] Los tests existentes de `newServer` y del shutdown pasan sin cambiar su semántica.

**Verification:**
- [ ] Tests pass: `go test ./cmd/orchestrator/... && make ci`
- [ ] Build succeeds: `make build`
- [ ] Manual check: comparar `newServer` con los defaults de config; sin levantar servicios.

**Dependencies:** Task 2 (paralelizable con Task 3)

**Files likely touched:** `cmd/orchestrator/main.go`, `cmd/orchestrator/main_test.go`

**Estimated scope:** Small (2 archivos)

### Checkpoint 2: Borde HTTP
- [ ] `make ci` y `make integration` verdes
- [ ] Contrato HTTP idéntico: mismos paths, métodos, headers y matriz de status
- [ ] Sin regresión de logs JSON a stdout

### Task 5: Extraer registro en Consul y tags de Traefik del composition root

**Description:** Crear `cmd/orchestrator/registration.go` con `buildRegistration`, `serviceID`
y `buildTags`, leyendo host público, intervalos del check, `deregisterTimeout` y parámetros de
Traefik desde `config.Config`. `main.go` queda como composition root. Factor 3 + SRP.

**Acceptance criteria:**
- [ ] `main.go` ya no declara `publicHost`, `consulCheckInterval`, `consulCheckTimeout`,
      `consulDeregisterAfter`, `deregisterTimeout`, `buildRegistration`, `serviceID` ni
      `buildTags`.
- [ ] Con los defaults, la lista de tags de Traefik es **byte a byte idéntica** a la actual
      (test que compara el slice completo, incluyendo el rule `Host(...) && Path(...)`).
- [ ] El desregistro usa `cfg.DeregisterTimeout` y no puede superar el presupuesto de
      `ORCH_SHUTDOWN_TIMEOUT` (documentar el tope en un comentario).

**Verification:**
- [ ] Tests pass: `go test ./cmd/orchestrator/... && make integration && make ci`
- [ ] Build succeeds: `make build`
- [ ] Manual check: diff de los logs de arranque — mismos `service_id`, `address` y `port`.

**Dependencies:** Tasks 2 y 4

**Files likely touched:** `cmd/orchestrator/registration.go` (nuevo),
`cmd/orchestrator/main.go`, `cmd/orchestrator/main_test.go`

**Estimated scope:** Medium (3 archivos)

### Checkpoint 3: Registro y descubrimiento
- [ ] Test de tags idénticos con defaults pasando
- [ ] `main.go` reducido a wiring + señales (sin lógica de Consul/Traefik)
- [ ] **Revisión humana** del diff de registro antes de seguir (alta probabilidad de romper
      el descubrimiento)

---

## Phase 2: Despliegue y CI

### Task 6: Compose con `read_only` y variables nuevas

**Description:** Declarar `read_only: true` en el servicio del Orquestador (el README ya lo
afirma) y propagar las variables nuevas con los mismos defaults del binario. Actualizar
`.env.example` con el bloque documentado, sin secretos reales. Factores 3, 6 y 10.

**Acceptance criteria:**
- [ ] `docker-compose.yml` incluye `read_only: true` y las variables nuevas con defaults
      idénticos a `config.go`; se conservan red `mired` externa, réplicas,
      `stop_grace_period` y ausencia de `ports:`/`container_name`.
- [ ] `.env.example` lista las variables nuevas comentadas, con una nota de que los defaults
      replican al binario.
- [ ] `docker compose config` resuelve sin error con `.env` copiado del ejemplo.

**Verification:**
- [ ] Build succeeds: `docker compose config --quiet`
- [ ] Manual check: `docker compose config | grep -A2 read_only`
- [ ] Manual check: `make integration` (escaneo anti-disco sigue verde con FS read-only)

**Dependencies:** Tasks 2 y 5

**Files likely touched:** `docker-compose.yml`, `.env.example`

**Estimated scope:** Small (2 archivos)

### Task 7: CI construye y escanea imagen sin publicarla

**Description:** Agregar a `.github/workflows/ci.yml` un job separado que haga
`docker build` con `VERSION`/`REVISION` y escanee con `grype ... --fail-on high`. Sin login,
sin push. `release.yml` queda como único publicador. Factores 5 y 10.

**Acceptance criteria:**
- [ ] El job nuevo corre `docker build` con build-args y ejecuta Grype sobre la imagen local;
      no incluye `docker/login-action` ni `push: true`.
- [ ] `release.yml` no se modifica y sigue publicando solo con tags `v*.*.*`.
- [ ] El job de imagen no introduce secretos nuevos y no bloquea al job `verify`.

**Verification:**
- [ ] Build succeeds: `docker build -t papersoul-orchestrator:local .`
- [ ] Manual check: `grype papersoul-orchestrator:local --fail-on high` (si Grype está
      instalado) o inspección del workflow en su defecto.
- [ ] Manual check: `git diff .github/workflows/` muestra cambios solo en `ci.yml`.

**Dependencies:** Task 1

**Files likely touched:** `.github/workflows/ci.yml`

**Estimated scope:** Small (1 archivo)

### Checkpoint 4: Despliegue y pipelines
- [ ] `docker compose config --quiet` y build local de imagen verdes
- [ ] Workflows revisados: CI no publica, release sí
- [ ] Sin regresión en `make ci`

---

## Phase 3: Cierre

### Task 8: Actualizar README, tabla de variables y matriz 12-Factor

**Description:** Documentar las variables nuevas, corregir la afirmación de `read-only` ahora
que el Compose la declara, cerrar la brecha de CI en "Brechas y pendientes", actualizar la
matriz de cumplimiento y dejar el contrato HTTP textualmente igual. Factor 12 y DRY
(documentación vs. código).

**Acceptance criteria:**
- [ ] La tabla de configuración incluye las 14 variables nuevas con su default y su efecto.
- [ ] La matriz 12-Factor y "Brechas y pendientes" reflejan el estado posterior al refactor;
      no queda ninguna afirmación de `read-only` sin sustento en el Compose.
- [ ] Las secciones *Contrato* y *Integración con Traefik* conservan los mismos paths,
      statuses, headers y comportamiento de liveness.

**Verification:**
- [ ] Tests pass: `make ci && make integration`
- [ ] Manual check: `git diff README.md` revisado línea por línea
- [ ] Manual check: `grep -nE '/ready|/metrics' README.md` solo encuentra la referencia al
      check de Persistencia, no al Orquestador.

**Dependencies:** Tasks 1-7

**Files likely touched:** `README.md`

**Estimated scope:** Medium (1 archivo extenso)

### Checkpoint 5: Entrega
- [ ] `make ci`, `make integration` y `docker build` verdes
- [ ] Documentación y configuración coinciden con el código
- [ ] Contrato HTTP y límites de rendimiento sin cambios (no se corren k6/Vegeta sin permiso)
- [ ] **Revisión humana final** y aprobación para commitear

---

## Definition of Done transversal (aplica a cada Task)

- [ ] Código formateado con `gofmt` y sin warnings de `go vet` (`make check-fmt`, `make vet`).
- [ ] `make test` y `make race` verdes.
- [ ] Errores al cliente siempre en `application/problem+json`; contrato OpenAPI intacto.
- [ ] Ninguna ruta de producción escribe a disco ni usa S3/MinIO.
- [ ] Logs JSON a stdout con `correlation_id`, sin rotación ni archivos.
- [ ] Sin cambios fuera de `PaperSoul-Microservicio-Orquestador`.
- [ ] Un commit por tarea (8 commits), siguiendo el orden de `tasks/todo.md`.

## Paralelización segura

- Task 1 y Task 2 son independientes entre sí.
- Task 3 y Task 4 pueden ejecutarse en paralelo una vez cerrada Task 2.
- Task 6 y Task 7 pueden ejecutarse en paralelo tras sus dependencias.
- Task 8 va al final: consume el resultado de todas las anteriores.
