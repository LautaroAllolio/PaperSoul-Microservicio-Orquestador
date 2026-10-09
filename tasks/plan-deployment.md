# Plan: Despliegue y operación del Orquestador

## Objetivo y alcance

Preparar únicamente `PaperSoul-Microservicio-Orquestador` para ejecutarse en Docker,
integrarse con una instancia de Traefik administrada por separado, escalar varias
instancias y validar el comportamiento con los scripts de k6 y Vegeta provistos por el
usuario. Documentar cómo aplica el servicio los 12 factores.

No se agregarán ni modificarán archivos de los microservicios Extractor o Persistencia.
Tampoco se incorporará un contenedor de Traefik al Compose del Orquestador ni se
reescribirán los scripts de estrés del profesor.

## Situación actual observada

- El Orquestador es una aplicación Go que sirve una API HTTP sobre TCP, por defecto en
  `:8080`; no se observó un listener UDP.
- La configuración de runtime ya se carga mediante variables de entorno.
- Los logs estructurados JSON van a stdout y el proceso maneja SIGINT/SIGTERM con cierre
  graceful.
- Hay límites de tamaño y concurrencia por proceso; la deduplicación por checksum hace
  seguro repetir el procesamiento del mismo documento bajo el contrato actual.
- El repositorio del Orquestador no contiene Dockerfile ni Compose propios.
- El Orquestador expone `GET /health` como liveness (no readiness): responde 200 mientras
  atiende HTTP y no consulta Extracción ni Persistencia.
- El README existente describe el contrato de la API y su operación local.
- Se encontraron `tests/spike_tests.js` (k6), `tests/test_carga.txt` (targets en formato
  Vegeta) y los cuatro PDFs en `tests/stress/pdfs/`.
- Los scripts k6 y Vegeta se adaptaron al endpoint del Orquestador
  `POST /api/v1/documents/process`, que recibe `multipart/form-data` con el campo `file`.
  k6 carga los PDFs desde `tests/stress/pdfs/`; el generador Go crea los bodies multipart
  binarios que Vegeta necesita bajo `tests/vegeta-generated/`.

## Decisiones de diseño propuestas

- El Orquestador se construye y ejecuta como contenedor propio. Su Compose, si se usa,
  definirá solo el Orquestador; Traefik se ejecutará y desplegará por separado.
- Traefik descubrirá las instancias del Orquestador a través de una red externa compartida.
  La configuración del Orquestador no creará ni administrará esa red o el proceso Traefik.
- La API se publica como **HTTP sobre TCP**. Confirmado: el proceso abre un listener
  `net.Listen("tcp", ORCH_ADDR)` (`cmd/orchestrator/main.go`) y no existe ningún listener
  UDP. Traefik puede enrutar UDP, pero ese requerimiento **no aplica** al Orquestador.
- Se agregó `GET /health` como liveness del proceso: responde saludable cuando el
  Orquestador está sirviendo HTTP y no consulta Extracción ni Persistencia. No se agrega
  readiness en esta etapa (implementado en `internal/handler/router.go`).
- Traefik usará `GET /health` para comprobar liveness. La indisponibilidad de una instancia
  puede excluirla de solicitudes posteriores; no se asumirá que Traefik siempre repite una
  solicitud fallida en otra instancia.
- Política de reintento/circuit breaker acordada (ver *Contrato de despliegue*): reintentar
  **solo** cuando el backend no responde (fallo de conexión/sin respuesta), **2 reintentos**
  (`attempts=2`) con `initialInterval` de **500 ms**. Traefik v3.5 no reintenta `5xx` ni
  `POST` con cuerpo, así que esto no garantiza reejecución en otra instancia; la deduplicación
  por checksum deja el endpoint idempotente y seguro para reintentos del cliente.
- Los cuatro PDFs originales se conservan. Los archivos de carga se adaptan para probar el
  contrato del Orquestador, y Vegeta recibe cuerpos multipart generados bajo demanda sin
  duplicar ni transformar permanentemente esos PDFs.
- Los umbrales de rendimiento no se inventan: se usan los criterios del curso (ver
  *Criterios de aceptación de carga*).

## Contrato de despliegue

Decisiones acordadas para el despliegue. Los valores marcados como pendientes se confirman
en las tareas que los consumen.

| Aspecto | Decisión |
|---|---|
| Protocolo de la API | HTTP/1.1 sobre TCP. Sin UDP. |
| Descubrimiento Traefik | Docker provider (labels en el contenedor). |
| Red compartida | Externa, nombre **`mired`**; se referencia con `external: true` y el Compose no la crea ni la administra. |
| Puerto interno | **8080** (`ORCH_ADDR`). No se publica al host por defecto; lo expone Traefik. |
| Host/path enrutado | **`orquestador.universidad.localhost`** → **`/api/v1/documents/process`**, por el **entrypoint `https`**. |
| TLS | `tls=true` sin certresolver: el Traefik existente sirve su certificado estático (wildcard `*.universidad.localhost`) desde el default TLS store. |
| Health check | `GET /health` como liveness (sin readiness); en el balanceador: `interval=10s`, `timeout=3s`. |
| Retry | Lo que ofrece Traefik v3.5: reintenta solo cuando el backend no responde (fallo de conexión/sin respuesta). **2 reintentos** (`attempts=2`, es decir 3 requests) con `initialInterval=500 ms`. v3.5 **no** reintenta respuestas `5xx` (no existe el campo `status`) ni `POST` con cuerpo (no existe `retryNonIdempotentMethod`); por eso esto **no** garantiza reejecución en otra instancia. |
| Circuit breaker / réplica caída | Traefik deja de enviar tráfico a la instancia que falla su liveness. No se promete failover ni reejecución en otra instancia. |

## Criterios de aceptación de carga (benchmark de cátedra)

Meta: **superar** el microservicio de referencia de la cátedra. Los perfiles de los scripts
ya versionados coinciden con los del enunciado.

### A. Spike con k6 (modelo cerrado)

- Perfil: subida a 100 VUs en 10 s, 20 s sostenidos a 100 VUs, bajada de 10 s a 0 VUs.
  Coincide con `tests/spike_tests.js`.
- A superar (referencia de la cátedra, 40 s): **1.037** peticiones procesadas; throughput
  sostenido **25,35 req/s**; error **0,00%** (100% HTTP 200); **p50 = 1,88 s**,
  **p90 = 7,83 s**, **p95 = 8,80 s**, máximo **13,94 s**.
- Comando: `k6 run -e ORCH_BASE_URL=<url> tests/spike_tests.js`.

### B. Carga fija con Vegeta (modelo abierto)

- Perfil: **50 req/s** continuas durante **30 s** (1.500 solicitudes, rotando los 4 PDFs),
  timeout de cliente **30 s**. Los 4 PDFs y los targets ya están en `tests/test_carga.txt`.
- A superar (referencia de la cátedra): throughput efectivo completado **16,65 req/s**;
  peticiones exitosas **998/1.500 (66,53%)**; timeouts de cliente (código 0) **501 (33,40%)**;
  **p50 = 14,89 s**.
- Comandos:
  ```powershell
  $env:VEGETA_RATE = 50
  $env:VEGETA_DURATION = "30s"
  go run ./tests/prepare_vegeta.go
  vegeta attack -targets=tests/test_carga.txt `
    -rate=$env:VEGETA_RATE -duration=$env:VEGETA_DURATION -timeout=30s | vegeta report
  ```

**Versiones usadas:** `k6` v2.3.0 y `vegeta` v12.12.0 (la cátedra no fijó versiones).

## Aplicación de los 12 factores

| Factor | Verificación/criterio para el Orquestador |
|---|---|
| 1. Codebase | El despliegue y los cambios quedan limitados al repositorio Orquestador. |
| 2. Dependencies | La imagen instala dependencias declaradas en `go.mod`/`go.sum`; no depende de herramientas instaladas en el host. |
| 3. Config | URLs, límites, tiempos de espera, dirección de escucha y valores operativos vienen del entorno; secretos no se incorporan a la imagen. |
| 4. Backing services | Extractor, Persistencia y la red de Traefik se tratan como servicios conectables/configurables, sin modificar sus repositorios. |
| 5. Build, release, run | Construcción reproducible de la imagen y separación entre artefacto, configuración de release y ejecución. |
| 6. Processes | Cada instancia procesa requests sin estado local duradero ni almacenamiento de documentos en disco. |
| 7. Port binding | El proceso sigue escuchando en la dirección configurada; el puerto interno del contenedor se documenta y no se publica al host por defecto. |
| 8. Concurrency | Escalar mediante réplicas independientes; documentar el límite de concurrencia aplicable a cada instancia. |
| 9. Disposability | SIGTERM, apagado graceful y tiempo de gracia del contenedor son compatibles con `ORCH_SHUTDOWN_TIMEOUT`. |
| 10. Dev/prod parity | La ejecución local y la desplegada usan la misma imagen/configuración, evitando montajes o dependencias exclusivas de desarrollo. |
| 11. Logs | Los logs JSON continúan en stdout/stderr para que Docker y el stack externo los recolecten. |
| 12. Admin processes | No hay tareas administrativas actuales; documentar esa ausencia y, si aparece una, ejecutarla con el mismo artefacto y configuración. |

> **Matriz definitiva y evidencia verificada** (con archivo/línea por factor y brechas
> registradas): ver en `README.md` → *Cumplimiento de los 12 factores*.

## Tareas propuestas

Ver el orden, los criterios de aceptación y las verificaciones en
[`todo-deployment.md`](./todo-deployment.md). No comenzar implementación hasta que el
usuario revise el plan y se resuelvan las preguntas abiertas relevantes.

## Riesgos y mitigaciones

| Riesgo | Impacto | Mitigación |
|---|---|---|
| Se requiere UDP para una API que hoy es HTTP/TCP | Configuración incompatible o ruta que no funciona | **Resuelto:** la API es HTTP/TCP y no se requiere UDP. |
| Confundir reintentos con failover/circuit breaker | Solicitudes POST duplicadas o no repetidas como se espera | Retry acotado al backend sin respuesta (2 reintentos, 500 ms; Traefik v3.5 no reintenta 5xx ni POST con cuerpo); una réplica que falla su liveness se excluye del balanceo y no se promete reejecución en otra instancia. |
| Traefik y Orquestador no comparten red o proveedor de descubrimiento | Traefik no alcanza las réplicas | Usar Docker provider y la red externa `mired` sin crear otro Traefik. |
| `/health` confirma liveness, no disponibilidad de los downstream | Una instancia viva podría no poder completar procesamiento si falla una dependencia | Mantenerlo como liveness por decisión acordada; registrar métricas/errores downstream por separado y no interpretarlo como readiness. |
| No se especifican umbrales de rendimiento | No se puede concluir objetivamente si una corrida aprueba | **Resuelto:** umbrales provistos por la cátedra (ver *Criterios de aceptación de carga*). |

## Preguntas abiertas

Resueltas en la Tarea 1:

- **UDP:** no aplica; la API es HTTP sobre TCP y no hay listener UDP.
- **Retry / failover:** reintentar solo cuando el backend no responde (Traefik v3.5 no
  reintenta `5xx` ni `POST` con cuerpo), hasta 2 reintentos con `initialInterval` de 500 ms;
  una réplica no saludable se excluye del balanceo y no se asume reejecución en otra instancia.
- **Traefik:** Docker provider, red externa `mired`, host `orquestador.universidad.localhost`
  por el entrypoint `https` (TLS estático del default TLS store) y path
  `/api/v1/documents/process`.
- **Umbrales:** provistos por la cátedra (ver *Criterios de aceptación de carga*).

Resuelto:

- Versiones de `k6` (v2.3.0) y `vegeta` (v12.12.0) a usar; la cátedra no las especificó.

## Aprobación

La implementación comienza después de que el usuario revise este plan y aclare las
decisiones abiertas que afecten el contrato de red, retry/circuit breaker y pruebas de carga.
