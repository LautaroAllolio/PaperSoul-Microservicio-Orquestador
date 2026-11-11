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
- Docker Engine con Compose v2 para construir/publicar la imagen y levantar el stack local.
- Opcional: Consul (stack local) para `DISCOVERY=consul`; sin él se puede usar
  `DISCOVERY=static`.
- Opcional: `k6` y `vegeta` para las pruebas de carga, y `grype` para el escaneo de
  vulnerabilidades.

## Cómo correr

```bash
# Modo estático (sin Consul): las dos URLs downstream son obligatorias.
export DISCOVERY=static
export EXTRACTOR_URL=http://localhost:8000
export PERSISTENCE_URL=http://localhost:8081

# En desarrollo:
go run ./cmd/orchestrator

# Para un binario. Ojo: `make build` es solo una comprobación de que compila
# (`go build ./...` descarta el ejecutable al compilar varios paquetes).
go build -o bin/orchestrator ./cmd/orchestrator
./bin/orchestrator
```

Si no se define `DISCOVERY`, el binario arranca en `static`. En modo `consul`
las URLs fijas no se usan: alcanza con `DISCOVERY=consul` y
`CONSUL_HTTP_ADDR=http://consul:8500`, y el Orquestador se registra en Consul
con sus tags de Traefik.

### Con Docker

La imagen se construye desde este repositorio con un build multi-stage: compila
como root en la etapa `builder` (que se descarta) y ejecuta únicamente el binario
como usuario **no-root** sobre una base **distroless** (sin shell ni gestor de
paquetes). La configuración **se pasa en runtime**; no queda embebida en la imagen.

```bash
# Build local versionado con el SHA corto del commit (tag `dev-<sha>`).
make docker-build

# O fijá una versión explícita:
make docker-build VERSION=v0.1.0

# Modo estático (sin Consul):
docker run --rm -p 127.0.0.1:8080:8080 \
  -e DISCOVERY=static \
  -e EXTRACTOR_URL=http://extractor:9000 \
  -e PERSISTENCE_URL=http://persistencia:8000 \
  "papersoul-orchestrator:dev-$(git rev-parse --short HEAD)"
```

No se usan tags flotantes (`:local`/`:latest`): cada build queda identificado por una
versión o por el SHA del commit. Para publicar la imagen en un registry, `make docker-push
IMAGE=...` (requiere `docker login`). El release oficial se describe en *Publicar una
imagen versionada*.

En modo `consul` se reemplazan las dos URLs por
`-e DISCOVERY=consul -e CONSUL_HTTP_ADDR=http://consul:8500` (y el contenedor
necesita estar en la red de Consul). Las URLs downstream tienen que ser
resolubles **desde dentro del contenedor** (nombres de servicio en la red de
Compose, no `localhost` del host).

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
escalar a varias réplicas. La imagen se construye/publica aparte y se referencia con
`ORCH_IMAGE`, para poder desplegar distintas versiones sin rebuild:

```bash
make docker-build VERSION=v0.1.0
cp .env.example .env          # ORCH_IMAGE, DISCOVERY y CONSUL_HTTP_ADDR ya vienen listos
# Ajustá ORCH_IMAGE al tag que construiste (o al publicado en GHCR).
docker compose up -d

# escalar a N réplicas sin editar el archivo
docker compose up -d --scale orchestrator=3
```

`ORCH_IMAGE` es obligatoria: el Compose no tiene `build:` ni un default flotante. Si
falta, Compose falla con un mensaje explícito en vez de arrancar una imagen vieja. Para
producción conviene fijarla por digest (`ghcr.io/...@sha256:...`), no solo por tag.

El Compose fija `DISCOVERY=consul` por defecto y `CONSUL_HTTP_ADDR=http://consul:8500`.
En ese modo `EXTRACTOR_URL` y `PERSISTENCE_URL` son opcionales: no se usan, así
que pueden quedar vacías. Para integrar por etapas sin Consul, arrancá con
`DISCOVERY=static` y definí las dos URLs; sin ellas el arranque falla con un
mensaje explícito. Es requisito que exista la red `mired` (la crea el stack de
Traefik); este Compose no la crea ni la elimina.

Para probar el flujo completo en local está `docker-compose.dev.yml`, que agrega
los downstream Extractor y Persistencia a partir de sus imágenes ya construidas.
Consul, Traefik y MongoDB se levantan por separado desde sus propios stacks. El procedimiento
completo —MongoDB, red `mired`, variables, build, verificación y teardown— está en
*Puesta en marcha local (flujo completo)*.

### Publicar una imagen versionada

El repositorio no construye imágenes desde Compose. El release se dispara con un tag
semántico:

```bash
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` corre `make ci`, construye la imagen y la publica en
GHCR como `ghcr.io/lautaroallolio/papersoul-microservicio-orquestador`, con los tags
`0.1.0`, `v0.1.0`, `0.1` y `sha-<corto>`. El paquete nace privado: o se hace público
desde GitHub → Packages, o se ejecuta `docker login ghcr.io` antes de `docker compose up`.

Para un build local sin publicar: `make docker-build` (tag `dev-<sha>`). Para publicar
desde la máquina: `make docker-build VERSION=v0.1.0` y
`make docker-push IMAGE=ghcr.io/lautaroallolio/papersoul-microservicio-orquestador:v0.1.0`.

El Compose referencia las imágenes por tag; en producción conviene fijar el digest que
devuelve el registry (`docker inspect --format '{{index .RepoDigests 0}}' <imagen>`).

### Puesta en marcha local (flujo completo)

Para probar el flujo real —Traefik + MongoDB + Orquestador + Extractor + Persistencia—
en una máquina de desarrollo. Los comandos de Docker se ejecutan desde la raíz del
repositorio en WSL/Linux; el `curl` se puede correr desde PowerShell o WSL.

El orden es: **Consul → Traefik → MongoDB → los 3 microservicios**.

**Prerrequisitos**

- Docker Engine con Compose v2.
- Los stacks locales de Consul, Traefik y MongoDB (no forman parte de este repo).
  Consul se inicia independientemente desde `$HOME/docker/consul`, igual que Traefik
  y MongoDB desde sus directorios. Los tres stacks y las aplicaciones comparten la red
  externa `mired`. Traefik provee el puerto 443 y el certificado wildcard
  `*.universidad.localhost`; MongoDB queda accesible como `mongodb:27017` (lo usa
  Persistencia, no el Orquestador).
- `docker-compose.dev.yml` en la raíz agrega únicamente los servicios `extractor` y
  `persistencia` a partir de sus imágenes ya construidas.

**1. Variables de entorno**

Copiar el ejemplo y completar `MONGODB_URI` antes de usar Compose:

```bash
cp .env.example .env
```

El directorio `$HOME/docker/consul/config` debe contener la configuración local de
Consul. También se incluye [`consul/consul.json.example`](consul/consul.json.example)
como plantilla sin token. La configuración y los datos reales de Consul permanecen
en su stack independiente y no se copian a este repositorio.

**2. Levantar la infraestructura**

```bash
export TRAEFIK_STACK="$HOME/docker/traefik"
export MONGO_STACK="$HOME/docker/mongodb"
export CONSUL_STACK="$HOME/docker/consul"

# Red compartida (solo la primera vez)
docker network create mired 2>/dev/null || true

# 2.a Consul, iniciado desde su propio stack
(cd "$CONSUL_STACK" && docker compose up -d)
curl -fsS http://127.0.0.1:8500/v1/status/leader

# 2.b Traefik: ruteo, TLS y healthcheck del balanceador
(cd "$TRAEFIK_STACK" && docker compose up -d)

# 2.c MongoDB: lo consume Persistencia
(cd "$MONGO_STACK" && docker compose up -d)
```

Esperá a que MongoDB acepte conexiones antes de levantar Persistencia (si tarda,
Persistencia reintenta). Si ya tenés la infraestructura levantada, saltá al paso 3.
Las URLs se resuelven por nombre de servicio dentro de la red; no uses `localhost`.
El flujo completo usa descubrimiento por Consul:

```dotenv
DISCOVERY=consul
CONSUL_HTTP_ADDR=http://consul:8500
MONGODB_URI=mongodb://<usuario>:<clave>@mongodb:27017/papersoul?authSource=admin
ORCH_IMAGE=papersoul-orchestrator:dev-<sha>
ORCH_REPLICAS=2
EXTRACTOR_IMAGE=pdfextractor:v1.0.0
PERSISTENCIA_IMAGE=papersoul-persistencia:v1.0.0
```

Con `DISCOVERY=consul`, `EXTRACTOR_URL` y `PERSISTENCE_URL` quedan fuera: el
Orquestador encuentra las instancias en el catálogo. Para una prueba sin Consul,
cambiá a `DISCOVERY=static` y descomentá las dos URLs en `.env`. Las tres imágenes
se referencian por versión concreta: `ORCH_IMAGE` no tiene default y el override
local exige `EXTRACTOR_IMAGE` y `PERSISTENCIA_IMAGE`.

`MONGODB_URI` lo consume el override local y está comentada en `.env.example`:
descomentala y completá las credenciales.

**3. Construir las imágenes**

```bash
# Orquestador: tag versionado con el SHA (o VERSION=v0.1.0).
make docker-build

# Downstream: cada repo define su versión; no usar :latest.
docker build -t pdfextractor:v1.0.0 ../PaperSoul-Microservicio-Extractor
docker build -t papersoul-persistencia:v1.0.0 ../PaperSoul-Microservicio-Persistencia
```

El script `dev-up.sh` del proyecto padre construye tags `:local`/`:latest`; para el
flujo versionado usá los comandos de arriba y apuntá `ORCH_IMAGE`, `EXTRACTOR_IMAGE` y
`PERSISTENCIA_IMAGE` a esos tags. Una imagen vieja responde con un contrato viejo
aunque el código en disco ya esté corregido.

**4. Levantar los 3 microservicios**

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
docker compose -f docker-compose.yml -f docker-compose.dev.yml ps
```

Este comando levanta el Orquestador + Extractor + Persistencia sobre la red `mired`;
Consul, Traefik y MongoDB ya están arriba desde el paso 2. Quedan las réplicas del
Orquestador (`ORCH_REPLICAS`, default 2) más 3 de Extractor y 3 de Persistencia. El
Orquestador **no publica el 8080**: se llega únicamente por Traefik.

**5. Verificar el registro en Consul y el liveness**

Cada réplica se registra como `orchestrator` con un ID único
(`orchestrator-<hostname>-<puerto>`). El catálogo debe listar solo instancias
`passing`:

```bash
# Instancias sanas de las tres aplicaciones
curl -fsS 'http://127.0.0.1:8500/v1/health/service/orchestrator?passing' | jq 'length'
curl -fsS 'http://127.0.0.1:8500/v1/health/service/extractor?passing' | jq 'length'
curl -fsS 'http://127.0.0.1:8500/v1/health/service/persistencia?passing' | jq 'length'

# Detalle de una instancia: Address, Port, Tags y Check
curl -fsS 'http://127.0.0.1:8500/v1/health/service/orchestrator?passing' \
  | jq '.[0] | {Service: {ID: .Service.ID, Address: .Service.Address, Port: .Service.Port, Tags: .Service.Tags}, Checks: [.Checks[].Status]}'
```

El check es `GET /health` cada 3 s con timeout de 1 s; si una réplica queda
crítica más de 1 min, Consul la desregistra sola. Si el registro no aparece,
revisá `docker compose ... logs orchestrator` y que `CONSUL_HTTP_ADDR` apunte al
agente.

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
(cd "$HOME/docker/consul" && docker compose down)
(cd "$HOME/docker/traefik" && docker compose down)
(cd "$HOME/docker/mongodb" && docker compose down)
```

**Secuencia resumida** (con `.env` e imágenes ya listas):

```bash
(cd "$HOME/docker/consul" && docker compose up -d)
(cd "$HOME/docker/traefik" && docker compose up -d)
(cd "$HOME/docker/mongodb" && docker compose up -d)
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

**Si algo falla**

- `network mired declared as external, but could not be found` → crear la red.
- `Definí ORCH_IMAGE...` / `Definí EXTRACTOR_IMAGE...` / `Definí PERSISTENCIA_IMAGE...`
  → falta la imagen en `.env` o en el entorno. Construila con `make docker-build` y
  apuntá la variable al tag concreto; no hay default flotante.
- `pull access denied` o `unauthorized` al bajar de `ghcr.io` → el paquete está
  privado: `docker login ghcr.io` o cambiale la visibilidad en GitHub → Packages.
- `manifest unknown` → el tag no existe en el registry; verificá el release
  (`git tag`) o el nombre exacto de la imagen.
- `defina EXTRACTOR_URL` / `defina PERSISTENCE_URL` → estás en `DISCOVERY=static` y
  falta la variable en `.env`. En modo `consul` esas URLs no se piden.
- `CONSUL_HTTP_ADDR` / `DISCOVERY` en el error de arranque → falta la variable que
  exige el modo, o `DISCOVERY` no es `consul` ni `static`.
- 404 en la URL pública → falta el header `Host` o el path no es exactamente
  `/api/v1/documents/process`.
- `curl http://localhost:8080` no responde → el Compose no publica el puerto; usá
  Traefik o `docker run -p 8080:8080`.
- 502 `extractor-unavailable` / `persistence-unavailable` en modo `consul` →
  revisá que la instancia esté `passing` en Consul y que el check apunte al puerto
  correcto. Un fallo de lookup de Consul también se reporta como 502 (no 504): el
  504 queda reservado para el timeout del downstream.
- 502 intermitente con réplicas caídas → el catálogo cachea 1 s y Traefik refresca
  cada 5 s: durante esa ventana puede caer una request en una instancia muerta. El
  único reintento automático es por error de dial.
- 502/504 general → downstream no disponible:
  `docker compose -f docker-compose.yml -f docker-compose.dev.yml logs extractor persistencia orchestrator`.
- La instancia no aparece en Consul → el registro se reintenta con backoff en
  background; mirá los logs y que el contenedor esté en `mired`. Un registro nuevo
  puede tardar hasta `refreshInterval` (5 s) en reflejarse en Traefik.
- Error de certificado → `-k` en curl, `--insecure-skip-tls-verify` en k6,
  `-insecure` en Vegeta.

## Integración con Traefik

La instancia Traefik se despliega por separado —**este Compose no la inicia ni la
modifica**— y usa el provider **Consul Catalog** para los servicios registrados en
Consul. Mantiene también los providers Docker y File: Docker sigue exponiendo la UI de
Consul por sus labels. Las aplicaciones ya **no llevan labels de Traefik**: su ruteo
viaja como tags del registro en Consul.

La configuración estática que se agrega en
`$HOME/docker/traefik/config/traefik.yml` es (bloque **Apéndice A** del plan de
integración, sin token):

```yaml
providers:
  consulCatalog:
    endpoint:
      address: consul:8500
    exposedByDefault: false
    refreshInterval: 5s
```

Con `exposedByDefault: false`, un servicio sin `traefik.enable=true` se ignora: por
eso el Orquestador incluye esa tag en cada registro. El descubrimiento se basa en
polling por `refreshInterval` (más `providersThrottleDuration`, 2 s por defecto); no se
configura `watch`. El Compose local declara `traefik:v3.7.12`. Los puertos publicados de
Traefik y Consul se limitan a `127.0.0.1`; la UI de Consul también está disponible
bajo el host TLS `consul.universidad.localhost`.

El registro del Orquestador publica estas tags (idénticas en todas las réplicas):

```text
traefik.enable=true
traefik.http.routers.orchestrator.entrypoints=https
traefik.http.routers.orchestrator.rule=Host(`orquestador.universidad.localhost`) && Path(`/api/v1/documents/process`)
traefik.http.routers.orchestrator.tls=true
traefik.http.routers.orchestrator.middlewares=orchestrator-retry
traefik.http.services.orchestrator.loadbalancer.server.port=<puerto real del listener>
traefik.http.services.orchestrator.loadbalancer.healthcheck.path=/health
traefik.http.services.orchestrator.loadbalancer.healthcheck.interval=10s
traefik.http.services.orchestrator.loadbalancer.healthcheck.timeout=3s
traefik.http.middlewares.orchestrator-retry.retry.attempts=2
traefik.http.middlewares.orchestrator-retry.retry.initialinterval=500ms
```

Efecto en Traefik:

- **URL pública**: `https://orquestador.universidad.localhost/api/v1/documents/process`.
- **TLS**: el router usa el entrypoint `https` con `tls=true` **sin certresolver**: sirve el
  certificado estático del default TLS store (wildcard `*.universidad.localhost`). El
  entrypoint `http` del Traefik existente redirige a `https`.
- **Health check**: `GET /health` sobre el balanceador del servicio (`interval=10s`,
  `timeout=3s`). Es liveness del proceso, no readiness ni disponibilidad de los downstream:
  la réplica que falle queda fuera de la rotación y vuelve sola cuando recupera el liveness.
  El router público **no** expone `/health`: solo matchea
  `Host(...) && Path('/api/v1/documents/process')`. El healthcheck es interno, contra el
  puerto del contenedor. Es independiente del check de Consul (3 s/1 s) que decide si la
  instancia está `passing` en el catálogo.
- **Balanceo**: todas las réplicas se registran con el nombre de servicio `orchestrator`
  y un ID único (`orchestrator-<hostname>-<puerto>`); Consul Catalog las agrupa y Traefik
  reparte las requests entre las sanas. No hay `container_name` ni puertos publicados al
  host.
- **Retry**: middleware `orchestrator-retry`, `attempts=2` (**2 intentos totales = 1
  reintento**) con `initialInterval=500ms`. Traefik reintenta un error de **dial** —la
  conexión no llegó a enviar bytes— incluso para `POST`, y elige otra instancia si la hay.
  **No** reintenta respuestas `5xx` ni `503` en `POST`: para eso haría falta
  `retryNonIdempotentMethod=true` y bufferizar el body (`maxRequestBodyBytes`, 2 MB por
  defecto), lo que este endpoint no usa por el tamaño de los PDFs. La deduplicación por
  checksum deja el endpoint idempotente y seguro para reintentos del cliente.
- **IP anunciada**: el registro usa la IP del contenedor en `mired`. Si el contenedor
  tiene varias redes, definí `ORCH_ADVERTISE_ADDR`; si no, se autodetecta la primera IPv4
  no loopback.

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
| `DISCOVERY` | `static` (binario) / `consul` (Compose) | Cómo se encuentran los downstream: `consul` consulta el catálogo; `static` usa las URLs fijas y no toca Consul. Cualquier otro valor impide arrancar. |
| `CONSUL_HTTP_ADDR` | — | **Requerida en modo `consul`.** Base de la API HTTP de Consul (`http://consul:8500` o `consul:8500`). |
| `ORCH_ADVERTISE_ADDR` | autodetectada | IP con la que la instancia se anuncia en Consul. Solo hace falta si el contenedor tiene varias redes. |
| `EXTRACTOR_URL` | — | **Requerida solo en modo `static`.** Base URL del microservicio de Extracción. En `consul` no se usa. |
| `EXTRACTOR_TIMEOUT` | `30s` | Deadline de la llamada al Extractor. Vencido → 504. |
| `PERSISTENCE_URL` | — | **Requerida solo en modo `static`.** Base URL del microservicio de Persistencia. En `consul` no se usa. |
| `PERSISTENCE_TIMEOUT` | `15s` | Deadline de la llamada a Persistencia. Vencido → 504. |
| `ORCH_SHUTDOWN_TIMEOUT` | `10s` | Margen del cierre graceful tras SIGINT/SIGTERM (incluye el desregistro de Consul). |

En modo `consul` el Orquestador consulta `GET /v1/health/service/{nombre}?passing`,
cachea el catálogo 1 s por servicio, colapsa consultas concurrentes y reparte con
round-robin. Si Consul no responde, la llamada falla con 502 (no hay fallback
silencioso a las URLs fijas); el 504 queda reservado al timeout del downstream.

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
make docker-build
IMAGE="papersoul-orchestrator:dev-$(git rev-parse --short HEAD)"
grype "$IMAGE"
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
grype "$IMAGE" --fail-on high
```

`grype db update` actualiza la base de vulnerabilidades antes de escanear.

## Operación

- **Logs**: JSON a stdout (una línea por request: método, path, status, bytes, duración,
  `correlation_id`). El stack de los panic va al log, nunca al cliente.
- **Límites del servidor**: `ReadHeaderTimeout` 5s (slowloris) e `IdleTimeout` 60s. No hay
  `WriteTimeout` a propósito: la extracción puede tardar hasta `EXTRACTOR_TIMEOUT` y un timeout
  de escritura cortaría requests legítimos.
- **Cierre**: con SIGINT/SIGTERM deja de aceptar conexiones, espera hasta
  `ORCH_SHUTDOWN_TIMEOUT` a que terminen los requests en vuelo y, en modo `consul`,
  desregistra la instancia del catálogo (con un timeout propio de 2 s).
- **Liveness en `GET /health`**: responde 200 si el proceso atiende HTTP, sin consultar los
  downstream. Es liveness, no readiness: nunca debe usarse para asumir que el procesamiento
  va a funcionar. En modo `consul` es también el check que mantiene la instancia `passing`.
- **Reintentos**: no se repite una petición ya enviada (timeout, 5xx o error posterior al
  envío). La única excepción es un error de **dial** —la petición nunca salió—: se resuelve
  otra instancia, se reconstruye el request y se reintenta una sola vez. La deduplicación por
  checksum hace seguros los reintentos del cliente. El retry de Traefik aguas arriba cubre
  el mismo caso de dial (una vez) y elige otra réplica; no reintenta `5xx`/`503`.
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
| 2 | Dependencias (declaradas y aisladas) | Declaradas en `go.mod`/`go.sum`; se descargan solo en la etapa builder como capa cacheada; el runtime distroless lleva únicamente el binario, sin gestor de paquetes ni herramientas. CI resuelve Go desde `go.mod`. | `go.mod`; `Dockerfile:11-12` (`go mod download`), `Dockerfile:26` (distroless); `.github/workflows/ci.yml` (`go-version-file: go.mod`). |
| 3 | Config (en el entorno) | Toda la configuración operativa por variables de entorno (`internal/platform/config/config.go`). Fallo rápido si falta/inválida (`errors.Join`) y validación por modo: `static` exige las URLs, `consul` exige `CONSUL_HTTP_ADDR`. `.env` no se versiona; la imagen no embebe configuración (solo el `ORCH_ADDR` operativo por defecto); no hay secretos embebidos. | `internal/platform/config/config.go`; `Dockerfile:44`; `.gitignore:151` (`.env`); `.env.example`; `docker-compose.yml` (bloque `environment`, líneas 18-35). |
| 4 | Backing services (recursos adheridos) | Extracción y Persistencia se tratan como servicios conectables: en modo `consul` se descubren por el catálogo y en `static` por URL, sin distinguir local de remoto. La red `mired` es externa y Traefik se despliega aparte. Mongo queda dentro del repo de Persistencia (ajeno, no verificado aquí). | `cmd/orchestrator/main.go` (`newResolver`); `internal/platform/discovery/`; `internal/client/resolver.go`; `docker-compose.yml` (red `external: true`). |
| 5 | Build, release, run | Build reproducible por `Dockerfile` multi-stage; release = tag semántico que publica la imagen en GHCR (`release.yml`), referenciada luego por `ORCH_IMAGE` en Compose (separada del `run`); run = contenedor con configuración por entorno. La imagen lleva labels OCI con versión y commit. | `Dockerfile:28-38` (labels OCI); `.github/workflows/release.yml`; `Makefile` (`docker-build`/`docker-push`); `docker-compose.yml:16` (`image: ${ORCH_IMAGE:?...}`). |
| 6 | Processes (sin estado) | Proceso sin estado: cada request es dueño de su buffer y no hay estado compartido durable. **Cero almacenamiento intermedio** es un criterio arquitectónico garantizado por escaneo estático (go/ast sobre rutas de producción: sin `os.Create`/`CreateTemp`/`WriteFile`/`OpenFile`/`Remove*` ni SDKs de S3/MinIO). Corre con filesystem `--read-only`. | `internal/service/orchestrator.go:33-39` (doc "No tiene estado"); `internal/handler/process.go:48` (multipart en streaming, no vuelca a disco); `TestE2ENuncaEscribeADisco` en `internal/handler/integration_test.go:512`. |
| 7 | Port binding | Escucha en `ORCH_ADDR` (default `:8080`) con `net.Listen("tcp", ...)`; `EXPOSE 8080`; el puerto no se publica al host por defecto (lo expone Traefik). | `cmd/orchestrator/main.go:80`; `Dockerfile:46`; `docker-compose.yml` (sin `ports:`). |
| 8 | Concurrency (escalado por procesos) | Escala por réplicas independientes que se registran en Consul con el mismo nombre de servicio y ID único; Traefik las descubre y balancea. Por proceso, `ORCH_MAX_CONCURRENCY` (default 8) limita requests en vuelo con un semáforo: al saturarse responde `503` + `Retry-After: 1`, sin encolar; `/health` queda exento. | `internal/handler/middleware.go:139-161`; `cmd/orchestrator/main.go:155` (`buildRegistration`); `docker-compose.yml:38-39` (`deploy.replicas`). |
| 9 | Disposability | Arranque rápido (binario estático, no descarga nada en runtime). SIGTERM/SIGINT → deja de aceptar, `srv.Shutdown` espera hasta `ORCH_SHUTDOWN_TIMEOUT` y en modo `consul` desregistra la instancia; el `stop_grace_period` del contenedor supera ese timeout. Sin estado, una caída no requiere recuperación. | `cmd/orchestrator/main.go:238` (`serve`); `docker-compose.yml:37` (`stop_grace_period`); verificación de la Tarea 2 (`docker stop -t 15` con `ORCH_SHUTDOWN_TIMEOUT=10s`). |
| 10 | Dev/prod parity | Misma imagen y configuración en local y en el deploy; el override local referencia las **mismas imágenes** (sin `build:`). La única divergencia documentada es que el override agrega los downstream y Mongo locales, que en prod provee el stack de Traefik. | `docker-compose.yml` + `docker-compose.dev.yml` (layering de Compose). |
| 11 | Logs (flujo de eventos a stdout) | JSON a stdout (`slog.NewJSONHandler(os.Stdout)`): una línea por request con método, path, status, bytes, `duration_ms` y `correlation_id`; los panic loguean el stack al log, nunca al cliente; nada se escribe a archivos. | `cmd/orchestrator/main.go:51`; `internal/handler/middleware.go:81-99` (RequestLogger), `:106-131` (Recoverer); verificación de la Tarea 2 (logs JSON a stdout). |
| 12 | Admin processes | No hay tareas administrativas (migraciones, mantenimiento, cron) hoy; se documenta su ausencia. Cualquier tarea admin futura debe ejecutarse con el mismo artefacto y configuración. | No existe comando/módulo admin en `cmd/`; ausencia documentada en el plan de despliegue. |

### Brechas y pendientes

- **El build de la imagen no corre en cada PR**: `.github/workflows/ci.yml` valida solo el
  toolchain Go (`make ci`); el `docker build` y el push a GHCR viven en `release.yml` y se
  disparan con tags `v*.*.*`. Un error del Dockerfile se detecta recién al tagear.
  Recomendación: agregar un `docker build` sin push en PRs.
- **Validación final contra el Traefik real**: el balanceo/healthcheck se verificó con un Traefik
  desechable; falta confirmarlo contra la instancia del profesor (`orquestador.universidad.localhost`).
- **Fronteras ajenas no verificadas aquí**: el contrato del Extractor con `X-Document-Checksum` y el
  MongoDB de Persistencia son de otros repos (ver *Pendiente de coordinar*).
