# PaperSoul — Microservicio Orquestador y Validador de PDFs

Recibe un PDF por `multipart/form-data`, lo valida **íntegramente en memoria** (magic bytes,
límite de tamaño, estructura y cifrado con `pdfcpu`) y orquesta dos microservicios
downstream: **Extracción** (redirige el binario crudo) y **Persistencia** (guarda el
resultado).

**Cero almacenamiento intermedio**: el binario no se escribe a disco ni pasa por S3/MinIO.
Vive en un buffer acotado durante el request y se reutiliza para validar y para reenviar.
Todos los errores de HTTP salen como `application/problem+json` (RFC 9457).

El contrato completo está en [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1) y el
razonamiento de diseño en [`tasks/plan.md`](tasks/plan.md).

## Flujo

```
POST /api/v1/documents/process (multipart, campo file)
  └─ borde: MaxBytesReader → sniff %PDF- → límite de archivo   [400 · 413 · 422]
     └─ orquestador
        1. sha256(binario) → checksum
        2. GET Persistencia /by-checksum/{checksum}
           ├─ existe  → 200 REUSED      (no se extrae)
           └─ no existe
              3. pdfcpu: estructura y cifrado               [422]
              4. POST Extractor (multipart en streaming)     [502 · 504]
              5. POST Persistencia (JSON plano)              [409 → relectura → REUSED]
              → 200 PROCESSED
```

El endpoint es **idempotente por contenido**: reintentar tras un 502/504 es seguro, porque
la deduplicación por checksum evita el doble trabajo (y Persistencia devuelve 409 si otro
request insertó el mismo checksum primero, caso en el que el orquestador relee y responde
`REUSED`).

## Requisitos

- Go ≥ 1.25 (la versión de `pdfcpu` usada exige esa directiva en `go.mod`).

## Cómo correr

```bash
# Los dos downstream son obligatorios: el arranque falla sin ellos.
export EXTRACTOR_URL=http://localhost:8000
export PERSISTENCE_URL=http://localhost:8081

# En desarrollo:
go run ./cmd/orchestrator

# Para un binario. Ojo: `make build` es solo una comprobación de que compila
# (`go build ./...` descarta el ejecutable al compilar varios paquetes).
go build -o bin/orchestrator ./cmd/orchestrator
./bin/orchestrator
```

### Con Docker

La imagen se construye desde este repositorio con un build multi-stage: compila
como root en la etapa `builder` (que se descarta) y ejecuta únicamente el binario
como usuario **no-root** sobre una base **distroless** (sin shell ni gestor de
paquetes). `EXTRACTOR_URL` y `PERSISTENCE_URL` son obligatorias y **se pasan en
runtime**; no quedan embebidas en la imagen.

```bash
docker build -t papersoul-orchestrator:local .

docker run --rm -p 127.0.0.1:8080:8080 \
  -e EXTRACTOR_URL=http://extractor:9000 \
  -e PERSISTENCE_URL=http://persistencia:8000 \
  papersoul-orchestrator:local
```

Las URLs downstream tienen que ser resolubles **desde dentro del contenedor**
(nombres de servicio en la red de Compose, no `localhost` del host).

El proceso es PID 1 y ya maneja SIGTERM/SIGINT con cierre graceful, así que no
necesita un init externo. Al detenerlo, hay que dar un margen mayor que
`ORCH_SHUTDOWN_TIMEOUT` (default 10s):

```bash
docker stop -t 15 <container>
```

## Configuración

Todo se lee por variables de entorno en `internal/platform/config`. Si alguna es inválida el
proceso **no arranca**: reporta el error y sale con código 1, en vez de servir con defaults
equivocados.

| Variable | Default | Qué es |
|---|---|---|
| `ORCH_ADDR` | `:8080` | Dirección de escucha. |
| `ORCH_MAX_FILE_SIZE` | `26214400` (25 MiB) | Tope del archivo. Excederlo → 413. |
| `ORCH_MAX_BODY_BYTES` | `ORCH_MAX_FILE_SIZE` + 64 KiB | Tope duro del request completo (sobre multipart). |
| `ORCH_VALIDATION_RELAXED` | `true` | `true` = `ValidationRelaxed` de pdfcpu (tolera xrefs reconstruidas); `false` = `ValidationStrict`. |
| `ORCH_MAX_CONCURRENCY` | `8` | Requests en vuelo. Al saturarse responde 503 + `Retry-After: 1` (no encola). |
| `EXTRACTOR_URL` | — | **Requerida.** Base URL del microservicio de Extracción. |
| `EXTRACTOR_TIMEOUT` | `30s` | Deadline de la llamada al Extractor. Vencido → 504. |
| `PERSISTENCE_URL` | — | **Requerida.** Base URL del microservicio de Persistencia. |
| `PERSISTENCE_TIMEOUT` | `15s` | Deadline de la llamada a Persistencia. Vencido → 504. |
| `ORCH_SHUTDOWN_TIMEOUT` | `10s` | Margen del cierre graceful tras SIGINT/SIGTERM. |

Memoria: el pico por request es `ORCH_MAX_FILE_SIZE` más el contexto de pdfcpu, y
`ORCH_MAX_CONCURRENCY` acota el total. Subir el tamaño sin bajar la concurrencia sube el
pico de forma lineal.

## Contrato

`POST /api/v1/documents/process`

- **Body**: `multipart/form-data` con el campo obligatorio `file` (binario, `application/pdf`,
  máx 25 MiB).
- **`X-Correlation-Id`** (opcional): si viene, tiene que ser un UUID RFC 4122; si no lo es se
  descarta y se genera uno. El id con el que el servidor atendió el request vuelve en el
  header de respuesta, se refleja en el campo `instance` de cualquier `Problem` y se reenvía
  a Extracción y Persistencia.
- **200** (`application/json`): `documentId`, `status` (`PROCESSED` | `REUSED`), `checksum`
  (sha256 hex del binario) y `metadata` (`fileName`, `sizeBytes`, `pageCount`, `encrypted`).
  **No** incluye el texto extraído: ese queda en Persistencia.
- **Errores**: siempre `application/problem+json` (`type`, `title`, `status`, y según el caso
  `detail`, `instance`, `invalid_params`).

| Status | `type` | Cuándo |
|---|---|---|
| 400 | `…:invalid-multipart` | multipart malformado o falta el campo `file` (`invalid_params: file=required`). |
| 404 | `…:not-found` | Path inexistente. |
| 405 | `…:method-not-allowed` | Método no permitido (el header `Allow` dice cuál). |
| 413 | `…:file-too-large` | El archivo o el body exceden los topes. |
| 422 | `…:invalid-pdf-header` | El archivo no empieza con `%PDF-` (`invalid_params: file=invalid_magic_bytes`). |
| 422 | `…:invalid-pdf` | Estructura PDF inválida (pdfcpu). |
| 422 | `…:encrypted-pdf` | PDF cifrado (no soportado). |
| 500 | `…:internal-error` | Error no mapeado o panic. Nunca se filtra el stack. |
| 502 | `…:extractor-unavailable` / `…:persistence-unavailable` | Un downstream falló o devolvió algo fuera de contrato. |
| 503 | `…:overloaded` | Saturación de `ORCH_MAX_CONCURRENCY` (+ `Retry-After`). |
| 504 | `…:downstream-timeout` | Venció el deadline del downstream. |

Los `type` reales son URNs con prefijo `urn:papersoul:orchestrator:` (en la tabla se abbreviaron).

## Ejemplos

```bash
# PDF nuevo → 200 PROCESSED
curl -sS -X POST http://localhost:8080/api/v1/documents/process \
  -H "X-Correlation-Id: 3f2504e0-4f89-41d3-9a0c-0305e82c3301" \
  -F "file=@factura.pdf" | jq

# El mismo PDF otra vez → 200 REUSED (no se vuelve a extraer)
curl -sS -X POST http://localhost:8080/api/v1/documents/process \
  -F "file=@factura.pdf" | jq -r .status

# Un JPG → 422 invalid-pdf-header (falla sin leer el archivo completo)
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  http://localhost:8080/api/v1/documents/process -F "file=@foto.jpg"

# Sin campo file → 400 invalid-multipart
curl -sS -X POST http://localhost:8080/api/v1/documents/process -F "otro=@factura.pdf" | jq

# Archivo de 26 MiB → 413 file-too-large.
# Ojo: tiene que empezar con %PDF-, porque el sniff del magic bytes va ANTES que
# el cap de tamaño. Un archivo grande que no sea PDF devuelve 422.
{ printf '%%PDF-1.4\n'; head -c 27262976 /dev/zero; } > grande.pdf
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  http://localhost:8080/api/v1/documents/process -F "file=@grande.pdf"

# PDF válido con un downstream caído → 502 persistence-unavailable
curl -sS -X POST http://localhost:8080/api/v1/documents/process -F "file=@factura.pdf" | jq

# Path inexistente → 404 ; método incorrecto → 405 con Allow
curl -sS -o /dev/null -w '%{http_code}\n' http://localhost:8080/api/v1/nada
curl -si http://localhost:8080/api/v1/documents/process | head -1
```

## Desarrollo

| Target | Qué hace |
|---|---|
| `make build` | Compila. |
| `make test` | Tests unitarios y de contrato (sin red). |
| `make race` | Tests con el detector de carreras. |
| `make vet` | `go vet`. |
| `make integration` | Tests E2E con fakes downstream (`//go:build integration`). |
| `make lint-openapi` | Solo los tests del contrato (`api/openapi_test.go`). |
| `make fmt` / `make check-fmt` | Formatear / fallar si hay algo sin `gofmt`. |
| `make ci` | Todo lo anterior, en orden. |

```bash
make ci
```

Los tests de integración (`internal/handler/integration_test.go`) montan el cableado real de
`main.go` —router, orquestador, clients HTTP y validador pdfcpu— contra fakes de Extracción y
Persistencia también por HTTP, y cubren el flujo completo, el dedup, el 504 por timeout, el 413
y el escaneo que verifica que ninguna ruta de producción escribe a disco.

## Operación

- **Logs**: JSON a stdout (una línea por request: método, path, status, bytes, duración,
  `correlation_id`). El stack de los panic va al log, nunca al cliente.
- **Límites del servidor**: `ReadHeaderTimeout` 5s (slowloris) e `IdleTimeout` 60s. No hay
  `WriteTimeout` a propósito: la extracción puede tardar hasta `EXTRACTOR_TIMEOUT` y un timeout
  de escritura cortaría requests legítimos.
- **Cierre**: con SIGINT/SIGTERM deja de aceptar conexiones y espera hasta
  `ORCH_SHUTDOWN_TIMEOUT` a que terminen los requests en vuelo.
- **Sin endpoint de health**: el chequeo de vida es un `POST` al endpoint de procesamiento.
- **Sin reintentos automáticos** en v1: la deduplicación por checksum hace seguros los
  reintentos del cliente.
- **Sin autenticación** entre microservicios: fuera del alcance de esta fase.

## Pendiente de coordinar

- **Contrato del Extractor**: se asume `POST /api/v1/extractions` con `multipart/form-data`
  (parte `file` + campo `checksum`) y el header `X-Document-Checksum`. No hay OpenAPI del
  Extractor en este repo; si espera otros nombres, se ajusta en un commit chico.
- **Auth / rate-limit** entre microservicios en el deploy real: sin definir.

## Pruebas de carga

Los tests envían PDFs al endpoint `POST /api/v1/documents/process` en el campo multipart
`file`. Ejecutar desde la raíz del repositorio, con el Orquestador y sus servicios
downstream disponibles:

```powershell
k6 run tests/spike_tests.js
```

La URL predeterminada es `http://localhost:8080`. Para k6 puede cambiarse mediante
`ORCH_BASE_URL`, por ejemplo:

```powershell
$env:ORCH_BASE_URL = "https://orchestrator.universidad.localhost"
```

Vegeta consume un body estático por target. Antes de cada corrida, generar los cuerpos
multipart a partir de los PDFs del profesor:

```powershell
go run ./tests/prepare_vegeta.go
vegeta attack -targets=tests/test_carga.txt -duration=$env:VEGETA_DURATION -rate=$env:VEGETA_RATE | vegeta report
```

Los PDFs de entrada están en `tests/stress/pdfs/`. Los cuerpos generados se guardan en
`tests/vegeta-generated/`, carpeta ignorada por Git.
Definir `VEGETA_DURATION` y `VEGETA_RATE` con los valores indicados por el profesor. Ambas
herramientas ejercitan el endpoint del Orquestador.
