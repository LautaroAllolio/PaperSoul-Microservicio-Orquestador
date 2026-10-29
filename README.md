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

`GET /health` queda fuera de este flujo: es el chequeo de **liveness** del proceso y no toca
a Extracción ni a Persistencia.

## Requisitos

- Go ≥ 1.26 (`go.mod`); la imagen se compila con `golang:1.27-bookworm`.
- Docker Engine con Compose v2 para construir la imagen y levantar el stack local.
- Opcional: `k6` y `vegeta` para las pruebas de carga, y `grype` para el escaneo de
  vulnerabilidades.

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

`GET /health` responde 200 mientras el proceso atiende HTTP; sirve como liveness (lo
consultará Traefik), no como readiness de los downstream:

```bash
curl -sS http://localhost:8080/health   # {"status":"ok"}
```

Ese `curl` funciona con el `docker run -p` de arriba: con Compose el puerto 8080 no se
publica al host (ver *Puesta en marcha local*).

El proceso es PID 1 y ya maneja SIGTERM/SIGINT con cierre graceful, así que no
necesita un init externo. Al detenerlo, hay que dar un margen mayor que
`ORCH_SHUTDOWN_TIMEOUT` (default 10s):

```bash
docker stop -t 15 <container>
```

### Con Compose

`docker-compose.yml` despliega **solo el Orquestador** sobre la red externa
`mired` (la del stack de Traefik). No incluye Traefik ni los downstream, no
publica el puerto 8080 al host y no fija `container_name`, así que se puede
escalar a varias réplicas. La imagen se construye aparte y se referencia con
`ORCH_IMAGE`, para poder desplegar distintas versiones:

```bash
docker build -t papersoul-orchestrator:v1 .
cp .env.example .env          # completar EXTRACTOR_URL y PERSISTENCE_URL
ORCH_IMAGE=papersoul-orchestrator:v1 docker compose up -d

# escalar a N réplicas sin editar el archivo
docker compose up -d --scale orchestrator=3
```

`EXTRACTOR_URL` y `PERSISTENCE_URL` son obligatorias: si faltan, Compose falla
con un mensaje explícito. Es requisito que exista la red `mired` (la crea el
stack de Traefik); este Compose no la crea ni la elimina.

Para probar el flujo completo en local está `docker-compose.dev.yml`, que agrega
Extractor y Persistencia a partir de sus imágenes ya construidas. El procedimiento
completo —MongoDB, red `mired`, variables, build, verificación y teardown— está en
*Puesta en marcha local (flujo completo)*.

### Puesta en marcha local (flujo completo)

Para probar el flujo real —Traefik + MongoDB + Orquestador + Extractor + Persistencia—
en una máquina de desarrollo. Los comandos de Docker se ejecutan desde la raíz del
repositorio en WSL/Linux; el `curl` se puede correr desde PowerShell o WSL.

El orden es: **Traefik → MongoDB → los 3 microservicios**.

**Prerrequisitos**

- Docker Engine con Compose v2.
- Los stacks locales de Traefik y MongoDB (no forman parte de este repo). Traefik provee
  la red `mired`, el puerto 443 y el certificado wildcard `*.universidad.localhost`;
  MongoDB queda accesible como `mongodb:27017` (lo usa Persistencia, no el Orquestador).
- `docker-compose.dev.yml` en la raíz: agrega los servicios `extractor` y
  `persistencia` a partir de sus imágenes ya construidas.

**1. Levantar la infraestructura**

Ajustar las rutas a los stacks locales (`$HOME/docker/...` es la ubicación habitual en
este entorno):

```bash
export TRAEFIK_STACK="$HOME/docker/traefik"
export MONGO_STACK="$HOME/docker/mongodb"

# Red compartida (solo la primera vez)
docker network create mired 2>/dev/null || true

# 1.a Traefik: ruteo, TLS y healthcheck del balanceador
(cd "$TRAEFIK_STACK" && docker compose up -d)

# 1.b MongoDB: lo consume Persistencia
(cd "$MONGO_STACK" && docker compose up -d)
```

Esperá a que MongoDB acepte conexiones antes de levantar Persistencia (si tarda,
Persistencia reintenta). Si ya tenés ambos stacks levantados, saltá al paso 2.

**2. Variables de entorno**

```bash
cp .env.example .env
```

Completar en `.env` (las URLs se resuelven por nombre de servicio dentro de la red;
no uses `localhost`):

```dotenv
EXTRACTOR_URL=http://extractor:9000
PERSISTENCE_URL=http://persistencia:8000
MONGODB_URI=mongodb://<usuario>:<clave>@mongodb:27017/papersoul?authSource=admin
ORCH_IMAGE=papersoul-orchestrator:local
ORCH_REPLICAS=2
```

`MONGODB_URI` lo consume el override local y está comentada en `.env.example`:
descomentala y completá las credenciales.

**3. Construir las imágenes**

```bash
docker build -t papersoul-orchestrator:local .
docker build -t pdfextractor:latest ../PaperSoul-Microservicio-Extractor
docker build -t papersoul-persistencia:latest ../PaperSoul-Microservicio-Persistencia
```

El script `dev-up.sh` de la raíz solo construye las imágenes que **no existen**: si ya
hay una con el mismo tag, la reutiliza. Después de traer cambios de código hay que
forzar el rebuild (`./dev-up.sh --rebuild`) o reconstruir a mano el servicio que
cambió. Una imagen vieja responde con un contrato viejo aunque el código en disco ya
esté corregido.

**4. Levantar los 3 microservicios**

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
docker compose -f docker-compose.yml -f docker-compose.dev.yml ps
```

Este comando levanta Orquestador + Extractor + Persistencia sobre la red `mired`; Traefik
y MongoDB ya están arriba desde el paso 1. Quedan las réplicas del Orquestador
(`ORCH_REPLICAS`, default 2) más 3 de Extractor y 3 de Persistencia. El Orquestador
**no publica el 8080**: se llega únicamente por Traefik.

**5. Verificar el liveness**

El router público solo expone `/api/v1/documents/process`; `/health` se consulta
dentro de la red (la imagen distroless no trae shell ni `curl`):

```bash
docker run --rm --network mired curlimages/curl -sS http://orchestrator:8080/health
```

Respuesta esperada: `{"status":"ok"}`. La URL pública
`https://orquestador.universidad.localhost/health` devuelve 404 a propósito: no hay
router para ese path.

**6. Probar el endpoint**

Desde PowerShell (Windows):

```powershell
curl.exe -k -sS -X POST "https://127.0.0.1/api/v1/documents/process" `
  -H "Host: orquestador.universidad.localhost" `
  -F "file=@tests/stress/pdfs/Filosofia Lean.pdf;type=application/pdf"
```

Desde WSL:

```bash
curl -k -sS -X POST https://127.0.0.1/api/v1/documents/process \
  -H 'Host: orquestador.universidad.localhost' \
  -F 'file=@tests/stress/pdfs/Filosofia Lean.pdf;type=application/pdf'
```

- `-k` acepta el certificado wildcard del Traefik local.
- `Host` es el que usa el router de Traefik; sin ese header la respuesta es 404.
- La primera vez responde `PROCESSED`; si el PDF ya se procesó, `REUSED`. Para
  ejercitar el camino de extracción usá un PDF que todavía no hayas enviado.

Lo mismo se puede hacer desde Postman con la colección de [`postman/`](postman/README.md):
entorno *Traefik*, header `Host` y verificación SSL desactivada.

**7. Bajar el stack**

```bash
# Bajar los 3 microservicios
docker compose -f docker-compose.yml -f docker-compose.dev.yml down

# Bajar la infraestructura (opcional)
(cd "$HOME/docker/traefik" && docker compose down)
(cd "$HOME/docker/mongodb" && docker compose down)
```

**Secuencia resumida** (con `.env` e imágenes ya listas):

```bash
(cd "$HOME/docker/traefik" && docker compose up -d)
(cd "$HOME/docker/mongodb" && docker compose up -d)
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

**Si algo falla**

- `network mired declared as external, but could not be found` → crear la red.
- `defina EXTRACTOR_URL` / `defina PERSISTENCE_URL` / `Defina MONGODB_URI` → falta la
  variable en `.env`.
- 404 en la URL pública → falta el header `Host` o el path no es exactamente
  `/api/v1/documents/process`.
- `curl http://localhost:8080` no responde → el Compose no publica el puerto; usá
  Traefik o `docker run -p 8080:8080`.
- 502/504 → downstream no disponible:
  `docker compose -f docker-compose.yml -f docker-compose.dev.yml logs extractor persistencia orchestrator`.
- Error de certificado → `-k` en curl, `--insecure-skip-tls-verify` en k6,
  `-insecure` en Vegeta.

## Integración con Traefik

La instancia Traefik se despliega por separado —**este Compose no la inicia ni la
modifica**— y descubre a las réplicas por el **Docker provider** leyendo los labels del
contenedor:

- **URL pública**: `https://orquestador.universidad.localhost/api/v1/documents/process`.
- **TLS**: el router usa el entrypoint `https` con `tls=true` **sin certresolver**: sirve el
  certificado estático del default TLS store (wildcard `*.universidad.localhost`). El
  entrypoint `http` del Traefik existente redirige a `https`.
- **Health check**: `GET /health` sobre el balanceador del servicio (`interval=10s`,
  `timeout=3s`). Es liveness del proceso, no readiness ni disponibilidad de los downstream:
  la réplica que falle queda fuera de la rotación y vuelve sola cuando recupera el liveness.
  El router público **no** expone `/health`: solo matchea
  `Host(...) && Path('/api/v1/documents/process')`. El healthcheck es interno, contra el
  puerto del contenedor.
- **Balanceo**: las réplicas comparten el nombre de servicio del balanceador
  (`orchestrator`): Traefik las agrupa y reparte las requests entre las sanas. No hay
  `container_name` ni puertos publicados al host.
- **Retry**: middleware `orchestrator-retry`, `attempts=2` con `initialInterval=500ms`.
  Limitación de Traefik v3.5: reintenta solo cuando el backend no responde (fallo de
  conexión/sin respuesta), **no** reintenta `5xx` ni `POST` con cuerpo, así que **no** es una
  garantía de reejecución en otra instancia. El endpoint es seguro de reintentar igual por la
  deduplicación por checksum.

Para ejercitar el endpoint publicado, las herramientas usan la URL del host (ver *Pruebas de
carga*): alcanza con `ORCH_BASE_URL=https://orquestador.universidad.localhost` en k6, o
`ORCH_BASE_URL=https://127.0.0.1` + `ORCH_HOST_HEADER=orquestador.universidad.localhost` si
no hay resolución DNS.

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
| `ORCH_MAX_CONCURRENCY` | `8` | Requests en vuelo sobre `/api/v1/documents/process`. Al saturarse responde 503 + `Retry-After: 1` (no encola). `/health` no compite por slot. |
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

`GET /health` — **liveness** del proceso. Responde **200** `application/json` con
`{"status":"ok"}` mientras el Orquestador atiende HTTP. **No** consulta Extracción ni
Persistencia y **no es readiness**: una instancia puede devolver 200 y aun así fallar el
procesamiento si un downstream está caído. Por eso no se usa para decidir reintentos. Un
método distinto de `GET` responde 405 (tabla de abajo).

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

### Postman

La colección y los entornos están en [`postman/`](postman/README.md). Incluye:

- **Procesar PDF**: el flujo completo; hay que seleccionar el archivo en `Body → form-data → file`.
- **PDF inválido (422)** y **Sin campo file (400)**: casos de error determinísticos, sin elegir archivo.

Para el stack de Compose se usa el entorno *Traefik* (`https://127.0.0.1` + header
`Host: orquestador.universidad.localhost`) y se desactiva la verificación SSL. Para
`go run` alcanza con el entorno *Local directo* (`http://localhost:8080`).

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

### Escaneo de vulnerabilidades (Grype)

Usamos [Grype](https://github.com/anchore/grype) para revisar la imagen y las dependencias
contra vulnerabilidades conocidas. Ejecutar desde la raíz del repositorio.

**Imagen Docker del Orquestador** (SO base + binario):

```bash
docker build -t papersoul-orchestrator:local .
grype papersoul-orchestrator:local
```

**Filesystem del proyecto Go** (dependencias de `go.mod`/`go.sum`):

```bash
grype dir:.
```

**SBOM existente** (si ya lo generaste con Syft u otra herramienta):

```bash
grype sbom:./sbom.json
```

Para que el comando falle solo con severidades altas (útil en CI), `--fail-on` devuelve
código 2 cuando encuentra una vulnerabilidad de esa severidad o mayor:

```bash
grype papersoul-orchestrator:local --fail-on high
```

`grype db update` actualiza la base de vulnerabilidades antes de escanear.

## Operación

- **Logs**: JSON a stdout (una línea por request: método, path, status, bytes, duración,
  `correlation_id`). El stack de los panic va al log, nunca al cliente.
- **Límites del servidor**: `ReadHeaderTimeout` 5s (slowloris) e `IdleTimeout` 60s. No hay
  `WriteTimeout` a propósito: la extracción puede tardar hasta `EXTRACTOR_TIMEOUT` y un timeout
  de escritura cortaría requests legítimos.
- **Cierre**: con SIGINT/SIGTERM deja de aceptar conexiones y espera hasta
  `ORCH_SHUTDOWN_TIMEOUT` a que terminen los requests en vuelo.
- **Liveness en `GET /health`**: responde 200 si el proceso atiende HTTP, sin consultar los
  downstream. Es liveness, no readiness: nunca debe usarse para asumir que el procesamiento
  va a funcionar.
- **Sin reintentos automáticos en el servicio**: la deduplicación por checksum hace seguros
  los reintentos del cliente. El retry solo lo aporta Traefik aguas arriba (ver *Integración
  con Traefik*) y no garantiza reejecución en otra instancia.
- **Sin autenticación** entre microservicios: fuera del alcance de esta fase.

## Pendiente de coordinar

- **Contrato del Extractor**: se asume `POST /api/v1/extractions` con `multipart/form-data`
  (parte `file` + campo `checksum`) y el header `X-Document-Checksum`. No hay OpenAPI del
  Extractor en este repo; si espera otros nombres, se ajusta en un commit chico.
- **Auth / rate-limit** entre microservicios en el deploy real: sin definir.

## Pruebas de carga

Requieren `k6` y `vegeta` instalados en WSL, y Go para generar los cuerpos de Vegeta.
Ejecutar desde la raíz del repositorio, con el stack levantado y accesible por Traefik.
**No correr k6 y Vegeta al mismo tiempo.**

### k6 — spike de 100 VUs

```bash
k6 run --out web-dashboard --insecure-skip-tls-verify \
  -e ORCH_BASE_URL=https://127.0.0.1 \
  -e ORCH_HOST_HEADER=orquestador.universidad.localhost \
  tests/spike_tests.js
```

- `ORCH_BASE_URL` por defecto es `http://localhost:8080` (acceso directo al proceso).
- Contra el Traefik local se usa `https://127.0.0.1` y `ORCH_HOST_HEADER` para que el
  router reciba el `Host` correcto; el default del script ya es
  `orquestador.universidad.localhost`.
- `--insecure-skip-tls-verify` es por el certificado wildcard local.
- El dashboard queda en `http://localhost:5665/ui/`.

### Vegeta — 50 req/s durante 30 s

Primero generar los cuerpos multipart a partir de los PDFs de `tests/stress/pdfs/`:

```bash
set -o pipefail
go run ./tests/prepare_vegeta.go

RESULTS_DIR="/tmp/papersoul-vegeta-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RESULTS_DIR"

vegeta attack -rate=50 -duration=30s -targets=tests/test_carga.txt \
  -timeout=30s -insecure \
  -connect-to=orquestador.universidad.localhost:443:127.0.0.1:443 \
  | tee "$RESULTS_DIR/results.bin" | vegeta report

vegeta report -type=json < "$RESULTS_DIR/results.bin" > "$RESULTS_DIR/report.json"
vegeta plot < "$RESULTS_DIR/results.bin" > "$RESULTS_DIR/plot.html"
echo "Resultados en: $RESULTS_DIR"
```

- `tests/test_carga.txt` apunta a `https://orquestador.universidad.localhost`;
  `-connect-to` redirige ese dominio a `127.0.0.1:443` sin depender del DNS.
- `-insecure` acepta el certificado local.
- Revisar `Success`, `Status Codes` y latencias del reporte.

Los PDFs de entrada están en `tests/stress/pdfs/`. Los cuerpos generados se guardan en
`tests/vegeta-generated/`, carpeta ignorada por Git.

### Resultados (deploy local, downstream reales)

Entorno WSL2 (8 vCPU, 5,6 GiB), 4 PDFs precalentados, `ORCH_MAX_CONCURRENCY=128`, acceso por
Traefik HTTPS. Detalle y comparación con el benchmark en
[`tasks/plan-deployment.md`](./tasks/plan-deployment.md) → *Resultados de la campaña de carga*.

| Herramienta | Réplicas | req/s | éxito | p50 | p90 |
|---|---|---|---|---|---|
| k6 (spike 100 VUs) | 1 | 10,2 | 97,3 % | 1,62 s | 21,76 s |
| k6 (spike 100 VUs) | 2 | 12,7 | 99,2 % | 0,69 s | 19,13 s |
| Vegeta (50 rps × 30 s) | 1 | 25,98 | 59,9 % | 85 ms | 9,76 s |
| Vegeta (50 rps × 30 s) | 2 | 38,02 | 85,0 % | 789 ms | 8,57 s |

Con 2 réplicas, Vegeta **supera** la referencia de cátedra (16,65 req/s, 66,53 %, p50 14,89 s).
El throughput de k6 está limitado por la subida de los PDFs (hasta 8,9 MB) por el loopback
Docker/WSL2, no por el Orquestador.

Ambas herramientas ejercitan el endpoint del Orquestador a través de Traefik.

## Cumplimiento de los 12 factores

Matriz verificada contra el código, la imagen, el Compose y la configuración de runtime del
Orquestador. "Evidencia" apunta al archivo/línea que la sostiene; las brechas se registran al
final, sin ocultar lo que no está resuelto.

| # | Factor | Cumplimiento | Evidencia verificable |
|---|---|---|---|
| 1 | Codebase (una base de código) | Una base por microservicio. Todos los cambios de despliegue son del repo Orquestador; el Compose referencia imágenes y no modifica repos ajenos (el override local solo agrega downstream ya construidos). | Repo git; `docker-compose.yml` (sin `build:`); `docker-compose.dev.yml` (referencia imágenes de `../PaperSoul-*`). |
| 2 | Dependencias (declaradas y aisladas) | Declaradas en `go.mod`/`go.sum`; se descargan solo en la etapa builder como capa cacheada; el runtime distroless lleva únicamente el binario, sin gestor de paquetes ni herramientas. CI resuelve Go desde `go.mod`. | `go.mod`; `Dockerfile:10-12` (`go mod download`), `Dockerfile:26` (distroless); `.github/workflows/ci.yml` (`go-version-file: go.mod`). |
| 3 | Config (en el entorno) | Toda la configuración operativa por variables de entorno (`internal/platform/config/config.go`). Fallo rápido si falta/inválida (`errors.Join`). `.env` no se versiona; la imagen solo fija `ORCH_ADDR`; no hay secretos embebidos. Compose exige las URLs con `${VAR:?}`. | `internal/platform/config/config.go`; `Dockerfile:32`; `.gitignore:151` (`.env`); `.env.example`; `docker-compose.yml:20,22`. |
| 4 | Backing services (recursos adheridos) | Extracción y Persistencia se tratan como servicios conectables por URL (`EXTRACTOR_URL`/`PERSISTENCE_URL`), sin distinguir local de remoto. La red `mired` es externa y Traefik se despliega aparte. Mongo queda dentro del repo de Persistencia (ajeno, no verificado aquí). | `cmd/orchestrator/main.go:51-53` (clientes con `BaseURL` de cfg); `docker-compose.yml:64-69` (red `external: true`). |
| 5 | Build, release, run | Build reproducible por `Dockerfile` multi-stage; release = imagen referenciada por `ORCH_IMAGE` en Compose (separada del `run`); run = contenedor con configuración por entorno. | `Dockerfile`; `docker-compose.yml:13` (`image: ${ORCH_IMAGE:-...}`). *Brecha: ver abajo.* |
| 6 | Processes (sin estado) | Proceso sin estado: cada request es dueño de su buffer y no hay estado compartido durable. **Cero almacenamiento intermedio** es un criterio arquitectónico garantizado por escaneo estático (go/ast sobre rutas de producción: sin `os.Create`/`CreateTemp`/`WriteFile`/`OpenFile`/`Remove*` ni SDKs de S3/MinIO). Corre con filesystem `--read-only`. | `internal/service/orchestrator.go:33-39` (doc "No tiene estado"); `internal/handler/process.go:48` (multipart en streaming, no vuelca a disco); `TestE2ENuncaEscribeADisco` en `internal/handler/integration_test.go:512`. |
| 7 | Port binding | Escucha en `ORCH_ADDR` (default `:8080`) con `net.Listen("tcp", ...)`; `EXPOSE 8080`; el puerto no se publica al host por defecto (lo expone Traefik). | `cmd/orchestrator/main.go:60`; `Dockerfile:34`; `docker-compose.yml` (sin `ports:`). |
| 8 | Concurrency (escalado por procesos) | Escala por réplicas independientes que comparten servicio en Traefik. Por proceso, `ORCH_MAX_CONCURRENCY` (default 8) limita requests en vuelo con un semáforo: al saturarse responde `503` + `Retry-After: 1`, sin encolar; `/health` queda exento. | `internal/handler/middleware.go:139-161`; `docker-compose.yml:27-28` (`deploy.replicas`). |
| 9 | Disposability | Arranque rápido (binario estático, no descarga nada en runtime). SIGTERM/SIGINT → deja de aceptar y `srv.Shutdown` espera hasta `ORCH_SHUTDOWN_TIMEOUT`; el `stop_grace_period` del contenedor supera ese timeout. Sin estado, una caída no requiere recuperación. | `cmd/orchestrator/main.go:99-122`; `docker-compose.yml:26` (`stop_grace_period`); verificación de la Tarea 2 (`docker stop -t 15` con `ORCH_SHUTDOWN_TIMEOUT=10s`). |
| 10 | Dev/prod parity | Misma imagen y configuración en local y en el deploy; el override local referencia las **mismas imágenes** (sin `build:`). La única divergencia documentada es que el override agrega los downstream y Mongo locales, que en prod provee el stack de Traefik. | `docker-compose.yml` + `docker-compose.dev.yml` (layering de Compose). |
| 11 | Logs (flujo de eventos a stdout) | JSON a stdout (`slog.NewJSONHandler(os.Stdout)`): una línea por request con método, path, status, bytes, `duration_ms` y `correlation_id`; los panic loguean el stack al log, nunca al cliente; nada se escribe a archivos. | `cmd/orchestrator/main.go:36`; `internal/handler/middleware.go:81-99` (RequestLogger), `:106-131` (Recoverer); verificación de la Tarea 2 (logs JSON a stdout). |
| 12 | Admin processes | No hay tareas administrativas (migraciones, mantenimiento, cron) hoy; se documenta su ausencia. Cualquier tarea admin futura debe ejecutarse con el mismo artefacto y configuración. | No existe comando/módulo admin en `cmd/`; ausencia documentada en el plan de despliegue. |

### Brechas y pendientes

- **CI no construye ni publica la imagen**: `.github/workflows/ci.yml` corre solo el toolchain
  Go (`make ci`); el `docker build` y el versionado de la imagen (tag/registry) quedan fuera del
  pipeline. Recomendación: separar un job de build + push por tag semántico.
- **Validación final contra el Traefik real**: el balanceo/healthcheck se verificó con un Traefik
  desechable; falta confirmarlo contra la instancia del profesor (`orquestador.universidad.localhost`).
- **Fronteras ajenas no verificadas aquí**: el contrato del Extractor con `X-Document-Checksum` y el
  MongoDB de Persistencia son de otros repos (ver *Pendiente de coordinar*).
