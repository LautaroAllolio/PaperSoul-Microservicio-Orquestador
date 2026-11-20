# Implementation Plan: Refactorización 12-Factor del Orquestador

## Overview

Refactorizar `PaperSoul-Microservicio-Orquestador` para cerrar las brechas de los 12-Factor
App detectadas en la auditoría, sin tocar el rendimiento ni el contrato HTTP. El servicio ya
cumple la mayoría de los factores (stateless en memoria, distroless no-root, logs JSON a
stdout, graceful shutdown, semáforo, singleflight de Consul, escalado por réplicas); el
trabajo se concentra en **configuración externalizada**, **paridad toolchain/dev-prod**,
**higiene del codebase**, **separación de responsabilidades en el composition root** y
**build/CI sin publicación**.

**Congelado (no se negocia en este plan):**

- Motor: sniff de `%PDF-`, `MaxBytesReader`, `MultipartReader`, validación `pdfcpu`, SHA-256 y
  dedup por checksum, 409 → relectura → `REUSED`, multipart saliente con `ContentLength`.
- Concurrencia: semáforo `ORCH_MAX_CONCURRENCY` (503 + `Retry-After`, sin cola), `singleflight`
  del resolver de Consul, reintento **solo por fallo de dial**, pools de `http.Client`.
- Contrato: `POST /api/v1/documents/process`, `GET /health` (liveness), `problem+json` RFC 9457,
  `X-Correlation-Id` y statuses 400/404/405/413/422/500/502/503/504. **No se crean** `/ready`
  ni `/metrics`.
- Alcance: solo este repositorio. Quedan fuera Extractor, Persistencia, Traefik, Consul y
  MongoDB; `docker-compose.dev.yml` solo referencia sus imágenes.

> El plan de construcción original se archivó como `tasks/plan-build.md` y
> `tasks/todo-build.md` (decisión del usuario, 2026-11-20). Sus revisiones humanas y checks
> de CI pendientes no se dan por cerrados: simplemente dejan de ocupar los nombres canónicos.

## Architecture Decisions

- **Un único `config.Config`**: los valores hoy hardcodeados pasan a campos con *defaults
  idénticos a los actuales*. No se crean paquetes nuevos de configuración ni structs
  especulativos; se reutiliza `errors.Join` para fail-fast.
- **Extraer, no abstraer**: `buildRegistration`/`serviceID`/`buildTags` se mueven a
  `cmd/orchestrator/registration.go`. Es SRP, no una nueva interfaz ni una fábrica.
- **Consul Catalog sigue siendo la fuente de ruteo**: los tags de Traefik se generan en el
  registro; no se migran a labels Docker (se descarta por KISS y por paridad con el stack actual).
- **Defaults inmutables en esta iteración**: `DISCOVERY` sigue `static` en el binario y
  `consul` en Compose (documentado). Cambiar ese default rompería paridad; solo se hace
  explícito en documentación y `.env.example`.
- **CI construye y escanea, release publica**: `ci.yml` agrega un job de `docker build` +
  Grype sin login/push; `release.yml` sigue siendo el único publicador en GHCR.
- **Toolchain alineado**: `go.mod` pide Go 1.26.0; la imagen builder baja de `golang:1.27`
  a `golang:1.26-bookworm` (pdfcpu v0.15.0 requiere ≥1.25, así que 1.26 es suficiente).
- **Historial preservado**: el plan anterior sigue disponible como `plan-build.md` /
  `todo-build.md`; este plan usa los nombres canónicos y no modifica los archivos archivados.

## Dependency Graph

```
Task 1 (toolchain + .gitignore)     Task 2 (config.Config + tests)
          │                                   │
          │                    ┌──────────────┼──────────────┐
          │                    ▼              ▼              ▼
          │            Task 3 (Retry-After)  Task 4 (timeouts)  ...
          │                    └──────┬───────────┘
          │                           ▼
          │                   Task 5 (registro Consul/Traefik)
          │                           │
          └───────────┬───────────────┘
                      ▼
              Task 6 (Compose + .env.example)   Task 7 (CI build + Grype)
                      └──────────────┬──────────────────┘
                                     ▼
                             Task 8 (README + matriz 12-Factor)
```

El orden de implementación sigue el grafo de abajo hacia arriba: primero la base
(Tasks 1-2), luego los consumidores de configuración (Tasks 3-5), después despliegue/CI
(Tasks 6-7) y finalmente documentación (Task 8). Cada tarea deja el repo compilable y con
la suite verde.

## Task List

### Phase 0 — Fundamentos

- [x] **Task 1**: Alinear toolchain Go y reducir `.gitignore` a lo que Go necesita.
- [x] **Task 2**: Extender `config.Config` con los parámetros hoy hardcodeados.
- [x] **Checkpoint 1**: `make ci` + `docker build` verdes; fail-fast de config verificado.

### Phase 1 — Borde HTTP y composition root

- [x] **Task 3**: `Retry-After` del 503 configurable.
- [x] **Task 4**: Timeouts del `http.Server` desde configuración.
- [x] **Checkpoint 2**: contrato HTTP idéntico; `make ci` + `make integration` verdes.
- [x] **Task 5**: Extraer registro en Consul y tags de Traefik del composition root.
- [ ] **Checkpoint 3**: tags generados con defaults idénticos a los actuales; revisión humana.

### Phase 2 — Despliegue y CI

- [ ] **Task 6**: Compose con `read_only` y variables nuevas; `.env.example` alineado.
- [ ] **Task 7**: CI construye y escanea imagen sin publicarla.
- [ ] **Checkpoint 4**: `docker compose config` + build local; workflows revisados.

### Phase 3 — Cierre

- [ ] **Task 8**: Actualizar README, tabla de variables y matriz 12-Factor.
- [ ] **Checkpoint 5**: suite completa, documentación consistente, aprobación para commitear.

Los criterios de aceptación, verificación, dependencias y archivos de cada tarea están en
`tasks/todo.md`.

## Risks and Mitigations

| Riesgo | Impacto | Mitigación |
|---|---|---|
| Los tags de Traefik cambian y el balanceador deja de descubrir réplicas | Alto | Defaults exactamente iguales a los actuales + test que compara la lista completa de tags + Checkpoint 3 |
| `read_only: true` rompe una escritura accidental en runtime | Medio | Ya existe el escaneo AST anti-disco en `TestE2ENuncaEscribeADisco`; correr `make integration` antes y después |
| La validación estricta de las variables nuevas impide el arranque | Medio | Todos los defaults son válidos; tests de `config.Load()` con valores por defecto y con valores inválidos |
| Go 1.26 no compila la versión fijada de `pdfcpu` | Medio | `pdfcpu v0.15.0` declara ≥1.25; verificar con `docker build` en Task 1 antes de seguir |
| Grype falla por vulnerabilidades nuevas o base de datos caída | Bajo | Job separado con `--fail-on high`; no bloquea el job `verify`; se revisa en Checkpoint 4 |
| La sobrecarga de variables nuevas rompe KISS/YAGNI | Medio | Solo se externalizan valores que hoy son literales y dependen del entorno; se rechazan fábricas/structs especulativas |
| Se pierde el plan anterior con tareas sin cerrar | Medio | Archivado como `plan-build.md`/`todo-build.md` con `git mv`; historial preservado |

## Open Questions

**Resueltas (decisiones del usuario, 2026-11-20):**

1. ✔ `ORCH_RETRY_AFTER` es un entero en segundos (default `1`): el header HTTP `Retry-After`
   lo admite directamente y evita una conversión innecesaria.
2. ✔ El host público (`orquestador.universidad.localhost`) vive solo en el binario vía env;
   el Compose pasa el mismo default, sin duplicar la fuente de verdad.
3. ✔ No se agrega `/ready` ni `/metrics`: `/health` sigue siendo liveness por diseño y los
   downstream se resuelven por Consul.
4. ✔ CI no publica imagen; la publicación sigue solo en `release.yml` ante tag `v*.*.*`.
5. ✔ Verificación **solo estática**: no se levantan ni se modifican los stacks en `~/docker`
   (Traefik, Consul, MongoDB). Los checkpoints usan `make ci`, `make integration`,
   `docker build` y `docker compose config`.
6. ✔ Commits: **uno por tarea** (8 commits atómicos, sin agrupar).

## Verification (antes de implementar)

- [x] Plan escrito en `tasks/plan.md` y tareas en `tasks/todo.md` con aceptación, verificación,
      dependencias y scope.
- [x] Conflicto con el plan anterior resuelto por el usuario: archivado, no sobrescrito.
- [x] Ninguna tarea supera ~5 archivos.
- [x] Checkpoints entre fases y revisión humana en los puntos de riesgo (3 y 5).
- [x] Plan aprobado por el usuario; se puede ejecutar la Task 1.
