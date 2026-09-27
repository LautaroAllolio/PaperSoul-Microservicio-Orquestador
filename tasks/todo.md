# Task List — PaperSoul Orquestador y Validador de PDFs

Sigue la estructura del plan en `tasks/plan.md`. Orden por dependencias (bottom-up),
slices verticales. Cada tarea deja el repo en estado compilable.

Comandos base: `make build` · `make test` · `make race` · `make vet` · `make integration`
· `make lint-openapi` · `make check-fmt` · `make ci` (todo lo anterior en orden).

**Estado (Tasks 1-8 cerradas).** Las marcas `[x]` son las verificadas por la suite o
ejecutadas a mano. Quedan `[ ]` a propósito los ítems que necesitan a una persona: las
revisiones humanas de los checkpoints A, B y C, la aprobación del contrato OpenAPI, la
ráfaga de 30 requests simultáneos, y los dos puntos que solo existen en CI (el pipeline
está escrito pero el repo todavía no se pusheó, así que nunca corrió).

---

## Phase 0: Bootstrap

### Task 1: Esqueleto del módulo Go y estructura 3 capas

**Description:** Inicializa `go.mod` (módulo `github.com/papersoul/orchestrator`, Go ≥ 1.24),
dependencias (`chi/v5`, `pdfcpu` última estable), el árbol de directorios `cmd/internal/...`
y el `Makefile` con targets build/test/race/vet/integration.

**Acceptance criteria:**
- [x] `go build ./...` compila sin código de negocio (main.go mínimo + paquetes vacíos)
- [x] Árbol de directorios exacto del plan (handler/service/client/domain/platform)
- [x] `Makefile` con `build/test/race/vet/integration` funcionales

**Verification:**
- [x] Tests pass: `make test && make vet`
- [x] Build succeeds: `make build`
- [x] Manual check: `tree internal -L 2` muestra las capas

**Dependencies:** None

**Files likely touched:** `go.mod`, `go.sum`, `Makefile`, `cmd/orchestrator/main.go` (stub),
`internal/{handler,service,client,domain,platform}/*.go` (generadores de paquete)

**Estimated scope:** Small (3-5 files)

### Task 2: Config por env + modelado RFC 9457

**Description:** `platform/config` carga la configuración desde env (tabla 2.7 del plan) con
defaults; `platform/problem` implementa `Problem`/`InvalidParam`/`Write` (sección 2.1) y
`platform/errors` define los sentinels del dominio + `mapper.Map` (matriz 2.6).

**Acceptance criteria:**
- [x] `config.Load()` lee las 10 variables con defaults seguros y falla claro ante URL vacía de downstream
- [x] `Problem` serializa RFC 9457 exacto (campos obligatorios `type/title/status`, `omitempty` en resto)
- [x] Cada sentinel del dominio tiene entrada en `mapper.Map` y todo error no mapeado → `ErrInternal`(500)
- [x] `errors.Is` funciona para los sentinels documentados

**Verification:**
- [x] Tests pass: `go test ./internal/platform/...`
- [x] Build succeeds: `make build`
- [x] Manual check: `invalid_params` aparece en el JSON solo cuando hay items

**Dependencies:** Task 1

**Files likely touched:** `internal/platform/config/config.go`,
`internal/platform/problem/problem.go`, `internal/platform/errors/errors.go`,
`internal/platform/errors/mapper.go` + tests

**Estimated scope:** Medium (4-6 files)

---

## Phase 1: Validación (fail-fast)

### Task 3: Wrapper pdfcpu (estructura + cifrado) en memoria

**Description:** `platform/pdf.Validator` envuelve `api.ReadAndValidate(io.ReadSeeker, conf)`
con `ValidationRelaxed/Strict` según config; mapea errores a `ErrPDFCorrupted`/`ErrPDFEncrypted`
y detecta cifrado vía trailer (`ctx.XRefTable.Encrypt`). Fixtures en `testdata/` (PDF válido,
corrupto, cifrado). **Congelar aquí el mecanismo exacto de detección de cifrado.**

**Acceptance criteria:**
- [x] PDF válido → pageCount > 0, sin error, reader reposicionado al principio
- [x] PDF corrupto → `ErrPDFCorrupted` (`errors.Is`)
- [x] PDF cifrado → `ErrPDFEncrypted` (mecanismo congelado + test que lo fija contra la versión de go.mod)
- [x] No se escribe a disco: solo se usa `io.ReadSeeker` en memoria (revisión de imports: sin os.CreateTemp)

**Verification:**
- [x] Tests pass: `go test ./internal/platform/pdf/...`
- [x] Manual check: leer un PDF cifrado real (vía `api.Encrypt`) como fixture inicial

**Dependencies:** Task 2

**Files likely touched:** `internal/platform/pdf/validate.go`, `internal/platform/pdf/testdata/*`,
`internal/platform/pdf/validate_test.go`, `go.mod` (pdfcpu pinned cuando se congela el mecanismo)

**Estimated scope:** Medium (3-6 files)

### Task 4: Handler multipart streaming + magic bytes + límites

**Description:** `internal/handler/process.go` implementa la lectura **sin disco** del part
`file` (`r.MultipartReader()`), sniff de `%PDF-` en el primer chunk (abort early), caps
`MaxBytesReader` (413) + `LimitReader` (413), y responde `problem+json` para 400/413/422.
Contrato `handler.DocumentService` (interfaz consumidor) definido para que el orquestador
pueda implementarlo.

**Acceptance criteria:**
- [x] multipart válido → devuelve `*domain.ProcessInput{File: *bytes.Reader}` sin tocar disco
- [x] falta `file` → 400 problem+json con `invalid_params=[{file,required}]`
- [x] magic bytes ≠ `%PDF-` → 422 `invalid-pdf-header` (sin bufferizar el archivo completo)
- [x] part > `MaxFileSize` → 413; body > `MaxBodyBytes` → 413 (tope **25 MiB confirmado**)
- [x] el handler NO valida estructura pdfcpu (eso es del servicio); solo transporte

**Verification:**
- [x] Tests pass: `go test ./internal/handler/...`
- [x] Manual check (todo a problem+json): curl con un `.png` → 422; sin campo → 400; `dd` > 25MiB → 413

**Dependencies:** Task 2

**Files likely touched:** `internal/handler/process.go`, `internal/handler/services.go`,
`internal/handler/process_test.go`, `internal/domain/document.go`

**Estimated scope:** Medium (4-6 files)

### Checkpoint A: Validación end-to-end parcial
- [x] `make test && make vet` verde
- [x] Subir un JPG (422), un PDF de verdad (llega hasta el fake del punto siguiente) y un >25MiB (413)
- [ ] Revisión humana de la lectura `MultipartReader` en memoria (vía code review) antes de seguir

---

## Phase 2: Downstream y orquestación

### Task 5: Clientes HTTP Extract+Persistencia

**Description:** `internal/client`: `extractorClient.Extract` reenvía el binario en memoria con
multipart vía `io.Pipe` + `ContentLength` precalculado y headers `X-Document-Checksum`/
`X-Correlation-Id`; decodifica en **`domain.ExtractResponse`** (tipado; **no** `json.RawMessage`)
validando el esquema plano (`extraction_method` ∈ `{pymupdf, ocr}`, `page_count ≥ 1`).
`persistenceClient.FindByChecksum` → `GET /api/v1/documents/by-checksum/{checksum}` y `Store` →
`POST /api/v1/documents` con body **`domain.StoreDocumentRequest`**; mapeo 404→`ErrDocumentNotFound`,
409→`ErrPersistenceConflict`, no-2xx→`ErrUnavailable`, deadline→`ErrTimeout`. Fakes `httptest` para todo.

**Acceptance criteria:**
- [x] `Extract` produce petición multipart con boundary correcta y `ContentLength` exacto (>0), sin transfer-chunked
- [x] `Extract` con body no-JSON/vacío **o esquema inválido** (`extraction_method` ∉ {pymupdf,ocr}, `page_count < 1`) → `ErrExtractorInvalidResponse`
- [x] `FindByChecksum` 404 → `ErrDocumentNotFound`; 200 → `*StoredDocumentResponse` (ID/PDFHash/FileName/PageCount)
- [x] `Store` envía JSON plano `StoreDocumentRequest` (con `pdf_hash`, `text_hash` y `uploaded_at`); 409 → `ErrPersistenceConflict`; 201 → `StoredDocumentResponse`
- [x] timeout (fake que duerme) → `ErrExtractorTimeout`/`ErrPersistenceTimeout`
- [x] `X-Correlation-Id` y `X-Document-Checksum` propagados en todos los calls

**Verification:**
- [x] Tests pass: `go test ./internal/client/...`
- [x] Manual check: el fake registra `R.ContentLength` y `FormDataContentType()`

**Dependencies:** Task 3

**Files likely touched:** `internal/client/extractor.go`, `internal/client/persistence.go`,
`internal/client/multipart.go`, `internal/client/transport.go` + tests, `internal/domain/document.go`

**Estimated scope:** Medium (5-7 files)

### Task 6: Orquestador (dedup → validate → extract → store)

**Description:** `service.orchestrator.Process` implementa el flujo de la sección 2.4:
checksum SHA-256 → `FindByChecksum` (hit=REUSED sin extraer) → `validator.Validate` (422) →
`extractor.Extract` → **cálculo de `text_hash` = SHA-256(`ExtractedText`) y `UploadedAt` =
`time.Now().UTC()`** → ensamblado de **`domain.StoreDocumentRequest`** → `persistence.Store` →
PROCESSED; ante 409 relee y responde REUSED. Con mocks (manuales) de las tres interfaces.

**Acceptance criteria:**
- [x] dedup hit → status `REUSED` y el mock del extractor NO se invoca
- [x] flujo nuevo → status `PROCESSED` con `documentId/checksum/pageCount` correctos y el mock de `Store` recibe `StoreDocumentRequest` con `PDFHash == checksum`, `TextHash == sha256(extracted_text)`, `UploadedAt ≈ now UTC`
- [x] `ExtractResponse` devuelto inválido (method desconocido / page_count < 1) → errores mapeables 502 y NO se llama a `Store`
- [x] `Validate` falla → 422 y no se llama a downstream
- [x] `Store` devuelve 409 → se relee por checksum y se responde `REUSED`
- [x] errores de extractor/persistence se propagan como sentinels 502/504 al mapper
- [x] reader siempre re-sekeado a 0 antes de cada uso (sin lecturas desfasadas)

**Verification:**
- [x] Tests pass: `go test ./internal/service/...`
- [x] Manual check: table-driven test donde el dedup hit NO invoca el mock extractor (contador de llamadas = 0)

**Dependencies:** Tasks 4, 5

**Files likely touched:** `internal/service/orchestrator.go`, `internal/service/orchestrator_test.go`

**Estimated scope:** Medium (2-4 files)

### Checkpoint B: Orquestación lógica
- [x] `make test` verde con mocks
- [ ] Revisión humana del flujo de dedup/race (leer orchestrator.go completo)

---

## Phase 3: Wiring y E2E

### Task 7: Router (chi) + middleware + main + graceful shutdown

**Description:** `handler/router.go` monta `POST /api/v1/documents/process` y middleware
(request-id a partir de `X-Correlation-Id`, recoverer→500 problem+json, logger slog,
semáforo de concurrencia); `cmd/orchestrator/main.go` cablea config→clients→pdf→orchestrator→handler,
arranca con `signal.NotifyContext` y shutdown graceful.

**Acceptance criteria:**
- [x] el servidor acepta requests y responde problem+json correctos en todas las rutas de error
- [x] panic → 500 problem+json (recoverer) sin filtrar stack al cliente
- [x] `X-Correlation-Id` recibido se preserva en la respuesta y en `instance`
- [x] cierre graceful: al recibir SIGTERM termina requests en vuelo y corta en `SHUTDOWN_TIMEOUT`
- [x] semáforo `MaxConcurrency` degrada con 503 (problem+json) cuando está saturado

**Verification:**
- [x] Tests pass: `make test`
- [ ] Manual check: `kill -TERM <pid>` no corta requests en vuelo; concatenar 30 requests simultáneos

**Dependencies:** Task 6

**Files likely touched:** `internal/handler/router.go`, `internal/handler/middleware.go`,
`cmd/orchestrator/main.go`, `internal/config` (si hace falta `MaxBodyBytes` ajuste)

**Estimated scope:** Medium (4-5 files)

### Task 8: Integration E2E en memoria + lint del contrato

**Description:** Tests con build tag `integration`: servidor real + fakes downstream
(`httptest`) cubren PROCESSED, REUSED, timeout del extractor (504) y verificación de que
no hay escritura a disco; se valida `api/openapi.yaml` (parseo 3.1) y se agrega README de
operación (env vars, curls de ejemplo).

**Acceptance criteria:**
- [x] E2E PROCESSED: upload → fake extractor recibe multipart → fake persistencia recibe JSON → 200
- [x] E2E REUSED: estado precargado en el fake → 200 sin llamar al fake extractor
- [x] E2E timeout: fake extractor duerme > timeout → 504 problem+json
- [x] no hay llamadas a `os.CreateTemp`/S3 en paquetes de producción (escaneo en CI del tag)
- [x] `openapi.yaml` parsea sin error con parser OpenAPI 3.1 y responde schema `Problem` en todos los errores

**Verification:**
- [x] Tests pass: `go test -tags=integration ./...`
- [x] Manual check: correr el flujo completo con los dos fakes y revisar `Content-Length` del request al extractor

**Dependencies:** Task 7

**Files likely touched:** `internal/handler/integration_test.go` (`//go:build integration`),
`api/openapi.yaml`, `README.md`, `Makefile` (target integration + lint-openapi)

**Estimated scope:** Medium (4-6 files)

### Checkpoint C: Entrega
- [ ] `make test && make race && make vet && make integration` verde en CI
- [ ] Contrato OpenAPI 3.1 aprobado (array de `responses` de error ≡ matriz 2.6)
- [ ] Revisión humana final del diseño (plan.md) y del código antes de primer deploy

---

## Definition of Done transversal (aplica a cada Task)

- [x] Código formateado con `gofmt`, sin warnings de `go vet`
- [ ] `go test ./...` verde; tests con `-race` verde en CI
- [x] Errores expuestos al cliente SIEMPRE bajo RFC 9457 (`application/problem+json`), nunca texto plano
- [x] Ninguna ruta de producción escribe a disco ni usa S3/MinIO
- [x] Todo error sentinel agregado tiene fila en `mapper.Map` + test de `errors.Is`