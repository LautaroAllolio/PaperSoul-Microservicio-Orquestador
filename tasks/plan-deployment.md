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
- Actualmente no existe un endpoint de health en el Orquestador.
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
- La API actual debe publicarse como HTTP sobre TCP. Traefik puede enrutar UDP, pero una
  ruta UDP no transporta ni balancea este endpoint HTTP; ese requerimiento debe precisarse
  antes de implementarlo.
- Se agregará `GET /health` como liveness del proceso: responderá saludable cuando el
  Orquestador esté sirviendo HTTP y no consultará Extracción ni Persistencia. No se agregará
  readiness en esta etapa. Esta decisión está acordada; la implementación queda pendiente.
- La comprobación de salud, el reintento y el circuit breaker se configurarán en la
  integración de Traefik solo tras definir sus semánticas. Traefik usará `GET /health` para
  comprobar liveness. La indisponibilidad de una instancia puede excluirla de solicitudes
  posteriores; no se asumirá que Traefik siempre repite una solicitud fallida en otra
  instancia.
- Los cuatro PDFs originales se conservan. Los archivos de carga se adaptan para probar el
  contrato del Orquestador, y Vegeta recibe cuerpos multipart generados bajo demanda sin
  duplicar ni transformar permanentemente esos PDFs.
- No se fijarán umbrales de rendimiento arbitrarios: se usarán los criterios del curso o
  los que confirme el usuario.

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

## Tareas propuestas

Ver el orden, los criterios de aceptación y las verificaciones en
[`todo-deployment.md`](./todo-deployment.md). No comenzar implementación hasta que el
usuario revise el plan y se resuelvan las preguntas abiertas relevantes.

## Riesgos y mitigaciones

| Riesgo | Impacto | Mitigación |
|---|---|---|
| Se requiere UDP para una API que hoy es HTTP/TCP | Configuración incompatible o ruta que no funciona | Confirmar qué tráfico necesita UDP y si corresponde al Orquestador antes de definir routers. |
| Confundir reintentos con failover/circuit breaker | Solicitudes POST duplicadas o no repetidas como se espera | Definir códigos/errores reintentables, límite y espera; validar con requests idempotentes y pruebas de instancia caída. |
| Traefik y Orquestador no comparten red o proveedor de descubrimiento | Traefik no alcanza las réplicas | Confirmar cómo se ejecuta Traefik y documentar la red externa existente sin crear otro Traefik. |
| `/health` confirma liveness, no disponibilidad de los downstream | Una instancia viva podría no poder completar procesamiento si falla una dependencia | Mantenerlo como liveness por decisión acordada; registrar métricas/errores downstream por separado y no interpretarlo como readiness. |
| No se especifican umbrales de rendimiento | No se puede concluir objetivamente si una corrida aprueba | Obtener del curso los umbrales y el comando/versión de cada herramienta; no inventar criterios. |

## Preguntas abiertas

- ¿El Orquestador realmente debe recibir tráfico UDP? Si sí, ¿qué protocolo/endpoint UDP y
  qué cliente lo consume? El código actual únicamente expone HTTP sobre TCP.
- ¿Qué significa “reintentar y enviar a otra instancia”: volver a intentar solo ante error
  de conexión, también ante ciertos status HTTP, o excluir instancias no saludables para
  requests futuros? ¿Qué cantidad máxima y espera requiere el curso?
- ¿Traefik usa Docker provider y ya existe una red externa compartida? ¿Cuál es el nombre
  de la red y el dominio/path que debe enrutar?
- ¿Qué comando/versión de k6 y Vegeta y qué umbrales entrega el profesor para aprobar las
  pruebas?

## Aprobación

La implementación comienza después de que el usuario revise este plan y aclare las
decisiones abiertas que afecten el contrato de red, retry/circuit breaker y pruebas de carga.
