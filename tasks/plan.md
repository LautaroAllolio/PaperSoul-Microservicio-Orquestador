# Implementation Plan: PaperSoul — Microservicio Orquestador y Validador de PDFs

## Overview

Microservicio en Go (latest stable) que expone `POST /api/v1/documents/process`, recibe un
PDF binario vía `multipart/form-data`, lo valida **íntegramente en memoria** (magic bytes,
límite de tamaño, estructura y cifrado mediante `pdfcpu`) y orquesta dos microservicios
downstream: **Extracción** (redirige el binario crudo) y **Persistencia** (almacena el
resultado de extracción). **Cero almacenamiento intermedio**: no hay S3/MinIO ni escritura a
disco; el binario vive en un `bytes.Buffer` acotado durante el request y se reutiliza para
validación y reenvío. Todos los errores HTTP siguen **RFC 9457** (`application/problem+json`).

### Decisiones de contrato validadas con el usuario

1. **Respuesta 200** → `documentId + status + checksum + metadata` (filename, size, pages, encrypted).
   **Sin el texto extraído crudo** en el 200 (optimización de ancho de banda).
2. **Payload a Persistencia** → `StoreDocumentRequest` con esquema **plano y tipado** (no
   `json.RawMessage`) compatible con el modelo `PdfDocument` de MongoDB de PaperSoul; el
   orquestador calcula `pdf_hash`, `text_hash` y `uploaded_at`. El binario solo viaja
   cliente → orquestador → extractor.
3. **Deduplicación por checksum** (no header Idempotency-Key): se calcula SHA-256 del PDF y se
   consulta a Persistencia **antes** de extraer. Si existe → `REUSED` (no se extrae, se
   reaprovecha el resultado persistido). Si no → se extrae y persiste → `PROCESSED`.
4. **PDFs cifrados** → rechazados con 422.

## Architecture Decisions

- **3 capas** (mandato): `internal/handler` (Transport) → `internal/service` (Orquestación) → `internal/client` (Adapters HTTP salientes). `platform/` y `domain/` son hojas compartidas.
- **Interfaces definidas en el consumidor** (idioma Go: el handler declara la interfaz `DocumentService` que necesita; el `service.Orchestrator` implementa; el orquestador declara `ExtractorClient` y `PersistenceClient` que los clients implementan). Permite mocks en tests sin dependencias extra.
- **Streaming multipart sin disco**: `r.MultipartReader()` (prohibido `ParseMultipartForm`, que vuelca a tempfiles). Lectura del part a un `bytes.Buffer` con `http.MaxBytesReader` (cap duro del request) + `io.LimitReader` (cap del archivo) y **sniff de magic bytes en el primer chunk** (abort early, evita bufferizar no-PDFs).
- **`pdfcpu` en modo stream**: `api.ReadAndValidate(io.ReadSeeker, *model.Configuration)` con `model.ValidationRelaxed` (opcional Strict vía config). El `*bytes.Reader` es `io.ReadSeeker`: pdfcpu hace `Seek`/`Read` sin disco.
- **Reenvío al Extractor con `io.Pipe`** (writer: `multipart.Writer`; reader: `http.Request.Body`) y `ContentLength` precalculado para **evitar transfer-chunked** y no duplicar el buffer completo.
- **Budget de memoria**: 1 copia del binario por request + sobrecarga del context de pdfcpu. Mitigación con semáforo de concurrencia en el middleware.
- **Hilo único por request**: cada request es dueño de su buffer; los `bytes.Reader` se re-sekean; sin race dentro del request.
- **Timeouts downstream** por cliente HTTP con deadline de contexto; mapeo a 504. Sin retries automáticos en v1 (la dedup por checksum hace los reintentos del cliente seguros).
- **Nada de auth entre servicios** por ahora (fuera de alcance; se registra en Open Questions).

## Flujo de datos (secuencia)

```mermaid
sequenceDiagram
    participant C as Client
    participant H as handler (Transport)
    participant S as service.Orchestrator
    participant V as platform/pdf (pdfcpu)
    participant E as client.Extractor
    participant P as client.Persistence
    C->>H: POST /api/v1/documents/process (multipart, file=PDF)
    H->>H: MultipartReader() + MaxBytesReader + LimitReader + sniff "%PDF-"
    H-->C: 400/413/422 (problem+json) [fail temprano]
    H->>S: Process(ctx, domain.ProcessInput{File: *bytes.Reader,…})
    S->>S: sha256(binario) → checksum
    S->>P: FindByChecksum(checksum)
    alt checksum existe (dedup hit)
        P-->>S: StoredDocument{…} (sin extraer)
        S-->>C: 200 status=REUSED
    else checksum NO existe (ErrDocumentNotFound)
        S->>V: ReadAndValidate(reader) [estructura + cifrado]
        V--xS: ErrPDFEncrypted / ErrPDFCorrupted → 422
        S->>E: Extract(file, checksum, filename)
        E-->>S: domain.ExtractResponse{text, method, pageCount}
        S->>S: sha256(text) → text_hash; UploadedAt = now UTC
        S->>P: Store(domain.StoreDocumentRequest)
        P--xS: conflict (race) → FindByChecksum de nuevo → REUSED
        S-->>C: 200 status=PROCESSED
    end
```

## Fase 1: Spec — Contrato de API (ver `api/openapi.yaml`)

`POST /api/v1/documents/process`
- Request: `multipart/form-data`, campo obligatorio `file` (binary, `application/pdf`, máx 25 MiB).
- Header opcional `X-Correlation-Id` (uuid) → se refleja en `instance` y se propaga aguas abajo.
- 200: `ProcessDocumentResponse` (`application/json`).
- Errores: 400, 413, 422, 500, 502, 504 — todos `application/problem+json` con esquema `Problem`.
- Retry semantics: idempotente por contenido (checksum); documentado en el contrato.

## Fase 2: Diseño Técnico

### 2.1 Modelado RFC 9457 (Go)

`internal/platform/problem/problem.go`:

```go
// Package problem implementa RFC 9457 (application/problem+json).
package problem

// URNs de tipos de problema (espacio acotado por el orquestador).
const (
	TypeInvalidMultipart   = "urn:papersoul:orchestrator:invalid-multipart"
	TypeFileTooLarge       = "urn:papersoul:orchestrator:file-too-large"
	TypeInvalidPDFHeader   = "urn:papersoul:orchestrator:invalid-pdf-header"
	TypeInvalidPDF         = "urn:papersoul:orchestrator:invalid-pdf"
	TypeEncryptedPDF       = "urn:papersoul:orchestrator:encrypted-pdf"
	TypeExtractorUnavailable = "urn:papersoul:orchestrator:extractor-unavailable"
	TypePersistenceUnavailable = "urn:papersoul:orchestrator:persistence-unavailable"
	TypeDownstreamTimeout  = "urn:papersoul:orchestrator:downstream-timeout"
	TypeInternalError      = "urn:papersoul:orchestrator:internal-error"
)

// Problem es el cuerpo de error estándar RFC 9457.
type Problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Instance      string         `json:"instance,omitempty"`
	InvalidParams []InvalidParam `json:"invalid_params,omitempty"`
}

// InvalidParam es la extensión RFC 9457 para errores de validación contextual.
type InvalidParam struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Write serializa p como application/problem+json y setea status/Content-Type.
func Write(w http.ResponseWriter, r *http.Request, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
```

El `instance` siempre se puebla con el `X-Correlation-Id` (recibido o generado) para
trazabilidad sin exponer stack internos.

### 2.2 Estructura de directorios (3 capas)

```
.
├── cmd/orchestrator/main.go        # bootstrap: config, wiring de deps, router, graceful shutdown
├── internal/
│   ├── handler/                    # CAPA TRANSPORT (HTTP entrante, chi)
│   │   ├── router.go               # chi.Mux, rutas + middleware
│   │   ├── process.go              # POST /api/v1/documents/process
│   │   ├── middleware.go           # request-id, recoverer→problem, logger, semáforo
│   │   └── process_test.go
│   ├── service/                    # CAPA NEGOCIO / ORQUESTACIÓN
│   │   ├── orchestrator.go         # Process(): checksum→dedup→validate→extract→store
│   │   ├── orchestrator_test.go    # mocks de Extractor/Persistence
│   ├── client/                     # CAPA ADAPTERS (HTTP saliente)
│   │   ├── extractor.go            # reenvía binario → JSON de extracción
│   │   ├── persistence.go          # FindByChecksum / Store
│   │   ├── multipart.go            # helper io.Pipe + ContentLength
│   │   └── transport.go            # http.Client compartido, decode + mapeo de errores
│   ├── domain/                     # TIPOS COMPARTIDOS (hoja, sin deps)
│   │   └── document.go
│   └── platform/                   # SOPORTE (hoja)
│       ├── config/config.go        # config por env
│       ├── errors/errors.go        # sentinels + mapper error→Problem
│       ├── problem/problem.go      # RFC 9457 (2.1)
│       └── pdf/validate.go         # wrapper de pdfcpu (stream, sin disco)
├── api/openapi.yaml                # contrato OpenAPI 3.1 (Fase 1)
├── go.mod, go.sum
├── Makefile                        # build / test / race / lint / integration
├── tasks/plan.md, tasks/todo.md
```

Dependencias entre capas (unidireccionales, sin ciclos):

```
handler → service → client
handler → platform/{errors,problem,config}
service → domain, platform/pdf
client  → domain, platform/errors
```

### 2.3 Interfaces y contratos internos

`internal/domain/document.go` (tipos compartidos):

```go
package domain

import (
	"io"
	"time"
)

// ProcessInput es lo que el handler entrega al orquestador.
// File ES el binario en memoria (bytes.Reader): seekeable para pdfcpu y reenvío.
type ProcessInput struct {
	FileName string
	Size     int64
	File     io.ReadSeeker
}

// ProcessStatus distingue flujo completo vs. dedup hit.
type ProcessStatus string

const (
	StatusProcessed ProcessStatus = "PROCESSED"
	StatusReused    ProcessStatus = "REUSED"
)

// ProcessResult es la respuesta 200: documentId + status + checksum + metadata.
// NO incluye el texto extraído crudo (ancho de banda).
type ProcessResult struct {
	DocumentID string
	Status     ProcessStatus
	Checksum   string
	FileName   string
	Size       int64
	PageCount  int
	Encrypted  bool
}

// ExtractRequest es el contrato consumido por el cliente del Extractor.
type ExtractRequest struct {
	File     io.ReadSeeker
	FileName string
	Checksum string
	Size     int64
}

// ExtractResponse es la respuesta tipada del Extractor (sin pass-through genérico).
type ExtractResponse struct {
	ExtractedText    string `json:"extracted_text"`
	ExtractionMethod string `json:"extraction_method"` // "pymupdf" | "ocr"
	PageCount        int    `json:"page_count"`
}

// Métodos de extracción válidos (validación en el cliente del extractor).
const (
	ExtractionMethodPyMuPDF = "pymupdf"
	ExtractionMethodOCR     = "ocr"
)

// StoreDocumentRequest es el payload tipado hacia Persistencia: esquema plano
// estricto compatible con el modelo PdfDocument de MongoDB (PaperSoul).
// El orquestador calcula PDFHash, TextHash y UploadedAt.
type StoreDocumentRequest struct {
	FileName         string    `json:"filename"`
	ExtractedText    string    `json:"extracted_text"`
	ExtractionMethod string    `json:"extraction_method"`
	PageCount        int       `json:"page_count"`
	PDFHash          string    `json:"pdf_hash"`
	TextHash         string    `json:"text_hash"`
	UploadedAt       time.Time `json:"uploaded_at"`
}

// StoredDocumentResponse es la respuesta de Persistencia (GET by-checksum y POST).
type StoredDocumentResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	PDFHash   string `json:"pdf_hash"`
	FileName  string `json:"filename"`
	PageCount int    `json:"page_count"`
}
```

`internal/handler/process.go` — contrato del handler hacia el servicio (definido en el consumidor):

```go
package handler

// DocumentService es el contrato que el handler necesita del orquestador.
type DocumentService interface {
	Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error)
}
```

`internal/service/orchestrator.go` — contratos del orquestador hacia los adapters:

```go
package service

// ExtractorClient es el contrato del microservicio de Extracción.
type ExtractorClient interface {
	Extract(ctx context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error)
}

// PersistenceClient es el contrato del microservicio de Persistencia.
// FindByChecksum retorna ErrDocumentNotFound cuando el checksum no existe.
type PersistenceClient interface {
	FindByChecksum(ctx context.Context, checksum string) (*domain.StoredDocumentResponse, error)
	Store(ctx context.Context, in domain.StoreDocumentRequest) (*domain.StoredDocumentResponse, error)
}

// PDFValidator valida estructura/cifrado con pdfcpu en memoria.
type PDFValidator interface {
	Validate(r io.ReadSeeker) (pageCount int, err error)
}
```

`internal/client/extractor.go` (implementación del contrato):

```go
package client

func (c *extractorClient) Extract(ctx context.Context, in domain.ExtractRequest) (*domain.ExtractResponse, error) {
	body, totalLen, ct, err := NewMultipartPipe(in.File, in.FileName)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/extractions", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Document-Checksum", in.Checksum)
	req.ContentLength = totalLen // evita transfer-chunked

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errorsSvc.ErrExtractorTimeout
		}
		return nil, errorsSvc.ErrExtractorUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, mapDownstreamError(errorsSvc.ErrExtractorInvalidResponse, resp) // traduce problem+json del extractor
	}
	var out domain.ExtractResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errorsSvc.ErrExtractorInvalidResponse
	}
	// Validación de esquema plano: sin pass-through genérico.
	if out.ExtractionMethod != domain.ExtractionMethodPyMuPDF &&
		out.ExtractionMethod != domain.ExtractionMethodOCR {
		return nil, errorsSvc.ErrExtractorInvalidResponse
	}
	if out.PageCount < 1 {
		return nil, errorsSvc.ErrExtractorInvalidResponse
	}
	return &out, nil
}
```

`internal/client/persistence.go`:

```go
// FindByChecksum → GET /api/v1/documents/by-checksum/{checksum}   [confirmado]
//   200 → *domain.StoredDocumentResponse
//   404 → ErrDocumentNotFound (sentinela, no un error de orquestador)
//   otros no-2xx → ErrPersistenceUnavailable
// Store → POST /api/v1/documents  (JSON plano: StoreDocumentRequest)
//   201 → *domain.StoredDocumentResponse
//   409 → ErrPersistenceConflict (dedup race: otro request insertó el mismo checksum)
//   otros no-2xx → ErrPersistenceUnavailable
```

### 2.4 Orquestador

`internal/service/orchestrator.go`:

```go
type orchestrator struct {
	extractor   ExtractorClient
	persistence PersistenceClient
	validator   PDFValidator
}

func (o *orchestrator) Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, errorsSvc.ErrInternal
	}
	checksum, err := checksumSHA256(in.File) // lee y deja el reader al inicio
	if err != nil {
		return nil, errorsSvc.ErrInternal
	}

	stored, err := o.persistence.FindByChecksum(ctx, checksum)
	switch {
	case err == nil:
		return resultFrom(stored, domain.StatusReused, checksum, in), nil
	case !errors.Is(err, errorsSvc.ErrDocumentNotFound):
		return nil, err // ya es un sentinel mapeable (502/504)
	}

	// No existe: validación estructural profunda antes de gastar el extractor.
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, errorsSvc.ErrInternal
	}
	if _, err := o.validator.Validate(in.File); err != nil {
		return nil, err // ErrPDFCorrupted / ErrPDFEncrypted → 422
	}
	// El pageCount definitivo lo reporta el extractor (ExtractResponse.PageCount);
	// Validate es gate de guardia (estructura/cifrado), no fuente del contador.
	// TODO(Task 6): asertar consistencia pageCount(validator) == pageCount(extractor) en tests.

	ext, err := o.extractor.Extract(ctx, domain.ExtractRequest{
		File:     in.File, // el reader ya está al inicio tras Validate
		FileName: in.FileName,
		Checksum: checksum,
		Size:     in.Size,
	})
	if err != nil {
		return nil, err
	}

	// El orquestador calcula hash del texto y la fecha de subida.
	textHash := sha256.Sum256([]byte(ext.ExtractedText))

	stored, err = o.persistence.Store(ctx, domain.StoreDocumentRequest{
		FileName:         in.FileName,
		ExtractedText:    ext.ExtractedText,
		ExtractionMethod: ext.ExtractionMethod,
		PageCount:        ext.PageCount,
		PDFHash:          checksum,
		TextHash:         hex.EncodeToString(textHash[:]),
		UploadedAt:       time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, errorsSvc.ErrPersistenceConflict) {
			// Race de dedup: ganó otro request. Releer el resultado y reusar.
			stored, err = o.persistence.FindByChecksum(ctx, checksum)
			if err != nil {
				return nil, err
			}
			return resultFrom(stored, domain.StatusReused, checksum, in), nil
		}
		return nil, err
	}

	return resultFrom(stored, domain.StatusProcessed, checksum, in), nil
}

// resultFrom mapea StoredDocumentResponse → ProcessResult (200 sin texto crudo).
func resultFrom(stored *domain.StoredDocumentResponse, status domain.ProcessStatus, checksum string, in *domain.ProcessInput) *domain.ProcessResult {
	return &domain.ProcessResult{
		DocumentID: stored.ID,
		Status:     status,
		Checksum:   checksum, // = stored.PDFHash (verificado en tests)
		FileName:   stored.FileName,
		Size:       in.Size,
		PageCount:  stored.PageCount,
	}
}

// checksumSHA256: seek(0) → sha256.Sum256 via io.Copy → seek(0). Reusable a lo largo del request.
func checksumSHA256(r io.ReadSeeker) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

### 2.5 Estrategia de memoria y manejo de streams

**Regla de oro**: el binario existe en UNA copia en memoria (`bytes.Buffer` → `bytes.Reader`)
durante el ciclo del request. Prohibido `ParseMultipartForm`, `FormFile`, `os.CreateTemp`,
S3/MinIO.

1. **Entrada (pipeline de lectura del part)** — `internal/handler/process.go`:

```go
const magicPDF = "%PDF-"

// readFilePart decodifica el primer part con nombre "file" a un buffer acotado.
func (h *ProcessHandler) readFilePart(r *http.Request) (name string, data *bytes.Reader, size int64, err error) {
	mr, err := r.MultipartReader() // stream, NO vuelca a disco
	if err != nil {
		return "", nil, 0, errorsSvc.ErrInvalidMultipart
	}
	var buf bytes.Buffer
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return "", nil, 0, errorsSvc.ErrMissingFilePart
		}
		if err != nil {
			return "", nil, 0, errorsSvc.ErrInvalidMultipart
		}
		if part.FormName() != "file" {
			part.Close() // ignorar otros campos / partes (no: form-data extra)
			continue
		}
		// Sniff del magic bytes en el primer chunk: aborta sin bufferizar no-PDFs.
		br := bufio.NewReaderSize(part, 8)
		hdr, err := br.Peek(len(magicPDF))
		if err != nil || !bytes.Equal(hdr, []byte(magicPDF)) {
			return "", nil, 0, errorsSvc.ErrInvalidPDFHeader
		}
		// Cap por-archivo: MaxFileSize+1 para detectar el exceso.
		n, err := buf.ReadFrom(io.LimitReader(br, h.cfg.MaxFileSize+1))
		if err != nil {
			return "", nil, 0, errorsSvc.ErrInvalidMultipart
		}
		if n > h.cfg.MaxFileSize {
			return "", nil, 0, errorsSvc.ErrFileTooLarge
		}
		return part.FileName(), bytes.NewReader(buf.Bytes()), n, nil
	}
}
```

- `http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)` envuelve el body en el handler/middleware (cap duro del request: `MaxFileSize + ~64KiB` de overhead multipart ⇒ un `*http.MaxBytesError` → 413).
- Si el part supera `MaxFileSize` → `ErrFileTooLarge` → 413 (detectado por `n > MaxFileSize` gracias al `LimitReader(max+1)`).
- Peek falla con `io.EOF` si el archivo es truncado/incompleto → `ErrInvalidPDFHeader` (422).

2. **Validación pdfcpu (sin disco)** — `internal/platform/pdf/validate.go`:

```go
package pdf

func New(relaxed bool) *Validator {
	conf := model.NewDefaultConfiguration()
	if relaxed {
		conf.ValidationMode = model.ValidationRelaxed
	} else {
		conf.ValidationMode = model.ValidationStrict
	}
	return &Validator{conf: conf}
}

type Validator struct{ conf *model.Configuration }

func (v *Validator) Validate(r io.ReadSeeker) (int, error) {
	ctx, err := api.ReadAndValidate(r, v.conf) // lee+valida XRefTable, en memoria
	if err != nil {
		if isEncryptionErr(err) {
			return 0, errorsC.ErrPDFEncrypted
		}
		return 0, errorsC.ErrPDFCorrupted
	}
	// Detección explícita de cifrado vía trailer (Encrypt entry).
	if ctx.XRefTable.Encrypt != nil {
		return 0, errorsC.ErrPDFEncrypted
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errorsC.ErrInternal
	}
	return ctx.PageCount, nil
}
```

> `isEncryptionErr` es un matching frágil sobre el mensaje de pdfcpu ("decrypt"/"password").
> En el Task 3 se **congela el mecanismo exacto** contra la versión de `pdfcpu` fijada en
> `go.mod` (verificar si `api.ReadAndValidate` falla limpio ante cifrado o si `ctx.XRefTable.Encrypt`
> es suficiente). Mismo cuidado con el campo `PageCount` del `model.Context`.

3. **Salida hacia el Extractor (io.Pipe + ContentLength precalculado)** — `internal/client/multipart.go`:

```go
// NewMultipartPipe arma el body multipart en un io.Pipe y devuelve su longitud
// exacta para poder fijar ContentLength (evita transfer-chunked) sin duplicar
// el buffer del binario en memoria.
func NewMultipartPipe(file io.Reader, fileName string) (body io.Reader, totalLen int64, contentType string, err error) {
	const nl = "\r\n"

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	boundary := mw.Boundary()

	disp := fmt.Sprintf(
		"--%s%sContent-Disposition: form-data; name=%q; filename=%q%sContent-Type: application/pdf%s%s",
		boundary, nl, "file", fileName, nl, nl, nl)
	if _, err = mw.CreateFormFile("file", fileName); err != nil {
		return nil, 0, "", err
	}
	closing := fmt.Sprintf("%s--%s--%s", nl, boundary, nl)
	// No conocemos size de antemano aquí: la longitud total se fija en el caller
	// (Size ya está en domain.ExtractRequest). totalLen = pre + size + post.

	go func() {
		mw.WriteField("checksum", "") // placeholder: el caller rellena el field real
		_ = pw.Close()
	}()
	_ = pr
	return nil, 0, "", nil // esqueleto ilustrativo; implementación completa en Task 5
}
```

Implementación definitiva del pipe (Task 5): el goroutine del writer escribe el
`multipart.Writer` sobre `pw` (campos `checksum` + `file` desde el `bytes.Reader`),
cierra `mw`, cierra `pw`; cualquier error se propaga con `pw.CloseWithError`. El
`body` es `pr` (reader del pipe) y el caller fija:

```go
totalLen = int64(len(prefix(envelope) ante-limit)) + in.Size + int64(len(suffix))
req.ContentLength = totalLen
```

Con `ContentLength` conocido no se usa chunked, y el `http.Client` hace backpressure
sobre el pipe: solo se materializa en memoria lo que el socket consumió.

4. **Persistencia**: body JSON plano `StoreDocumentRequest` (`filename`, `extracted_text`,
   `extraction_method`, `page_count`, `pdf_hash`, `text_hash`, `uploaded_at`) — no reenvía el
   binario. Reusar el mismo `bytes.Reader` no aplica aquí (no se reenvía).

**Accounting de memoria por request (worst case):**
`MaxFileSize (25 MiB)` + context pdfcpu (≈ tamaño del XRefTable + streams decodificados; mitigable con `conf.DecodeAllStreams=false`) + envelope multipart outbound (KB). El semáforo `MaxConcurrency` del middleware acota el total: `MaxConcurrency × (MaxFileSize + pdfcpu overhead)`.

### 2.6 Matriz de errores y mapeo RFC 9457

`internal/platform/errors/errors.go`: sentinels con `errors.New` (comparables con `errors.Is`).
`mapper.go`: `Map(err) problem.Problem` centraliza sentinel → `{type,title,status,detail,invalid_params}`.

| Sentinel (dominio) | ¿Dónde se origina? | HTTP | `type` (urn:papersoul:orchestrator:) | title (es) | invalid_params (si aplica) |
|---|---|---|---|---|---|
| `ErrInvalidMultipart` | handler (multipart malformado) | 400 | `invalid-multipart` | Petición inválida | — |
| `ErrMissingFilePart` | handler (sin campo `file`) | 400 | `invalid-multipart` | Petición inválida | `file: required` |
| `ErrFileTooLarge` | handler (`LimitReader` / MaxBytes) | 413 | `file-too-large` | Archivo demasiado grande | `file: max_size_exceeded` |
| `ErrInvalidPDFHeader` | handler (magic bytes) | 422 | `invalid-pdf-header` | El archivo no es un PDF válido | `file: invalid_magic_bytes` |
| `ErrPDFCorrupted` | platform/pdf (pdfcpu) | 422 | `invalid-pdf` | El documento PDF está corrupto | — |
| `ErrPDFEncrypted` | platform/pdf (trailer Encrypt) | 422 | `encrypted-pdf` | El documento PDF está cifrado | — |
| `ErrExtractorUnavailable` | client (conexión fallida) | 502 | `extractor-unavailable` | El servicio de extracción no está disponible | — |
| `ErrExtractorTimeout` | client (deadline excedido) | 504 | `downstream-timeout` | Tiempo de espera agotado | — |
| `ErrExtractorInvalidResponse` | client (body no-JSON/vacío/5xx) | 502 | `extractor-unavailable` | El servicio de extracción devolvió una respuesta inválida | — |
| `ErrPersistenceUnavailable` | client (conexión fallida / no-2xx) | 502 | `persistence-unavailable` | El servicio de persistencia no está disponible | — |
| `ErrPersistenceTimeout` | client (deadline) | 504 | `downstream-timeout` | Tiempo de espera agotado | — |
| `ErrDocumentNotFound` | client Persistence (404) | **interno** — se usa de control de flujo en el orquestador, nunca se expone | — | — | — |
| `ErrPersistenceConflict` | client Persistence (409, race) | **interno** — el orquestador relee y reusa (→200 REUSED) | — | — | — |
| `ErrInternal` | cualquier capa (panic/otro) | 500 | `internal-error` | Error interno del servidor | — |

Reglas del mapper:
- Toda sentinel no contemplada y todo error no sentinel ⇒ `ErrInternal` (500) sin detalle interno en `detail` (seguridad: no filtrar stack).
- `instance` = `urn:uuid:<X-Correlation-Id>` siempre.
- Los `invalid_params` se emiten con `name=file` (parte multipart) y `reason` UPPER_SNAKE.
- `writeFail` tras headers ya enviados ⇒ el recoverer/logger lo registra, no duplica respuesta.

### 2.7 Configuración (`internal/platform/config`)

Se lee por env en `config.Load()` con defaults seguros:

| Variable | Default | Notas |
|---|---|---|
| `ORCH_ADDR` | `:8080` | bind HTTP |
| `ORCH_MAX_FILE_SIZE` | `26214400` (25 MiB) | cap del part — **confirmado** con negocio |
| `ORCH_MAX_BODY_BYTES` | `MaxFileSize+65536` | cap duro del request (MaxBytesReader) |
| `ORCH_VALIDATION_RELAXED` | `true` | `model.ValidationRelaxed` vs Strict |
| `ORCH_MAX_CONCURRENCY` | `8` | semáforo de memoria |
| `EXTRACTOR_URL` | — | base URL del extractor |
| `EXTRACTOR_TIMEOUT` | `30s` | deadline del cliente |
| `PERSISTENCE_URL` | — | base URL de persistencia |
| `PERSISTENCE_TIMEOUT` | `15s` | deadline del cliente |
| `ORCH_SHUTDOWN_TIMEOUT` | `10s` | graceful shutdown |

### 2.8 Plan de pruebas y validación

**Unit tests** (por capa, sin red, mocks hand-written sobre las interfaces):
- `platform/problem`: serialización RFC 9457 (todos los campos, omitempty, invalid_params).
- `platform/pdf`: fixtures PDF reales en `testdata/` (PDF válido pequeño — generable con el propio `pdfcpu.Create` en un test helper—, PDF corrupto, PDF cifrado vía `api.Encrypt`).
- `platform/errors`: tabla `errors.Is` + cobertura completa de `mapper.Map`.
- `handler`: `httptest.NewRecorder` + `chi/testutil`; casos: multipart OK, sin `file` (400), magic bytes malos (422), archivo > límite (413), body > MaxBodyBytes (413), shape exacta del `Problem`.
- `client`: `httptest.NewServer` fake; verifica método/URL, `Content-Type` multipart + boundary, `ContentLength`, header `X-Document-Checksum`, `X-Correlation-Id` propagado, mapeo 404→ErrDocumentNotFound, 409→ErrPersistenceConflict, 5xx→ErrUnavailable, body no JSON/esquema inválido (`extraction_method` ∉ {pymupdf,ocr}, `page_count < 1`)→ErrExtractorInvalidResponse, deadline→ErrTimeout; en `Store` el fake asevera el JSON plano de `StoreDocumentRequest` (pdf_hash/text_hash/uploaded_at presentes).
- `service/orchestrator`: mocks de `ExtractorClient`/`PersistenceClient`/`PDFValidator`; casos: dedup hit (no llama al extractor, status REUSED), flujo completo (PROCESSED con `StoreDocumentRequest` donde `PDFHash==checksum`, `TextHash==sha256(text)`, `UploadedAt≈now UTC`), corrupto/encifrado (422, no llama downstream), race de checksum (409→re-read→REUSED), fallos extractor/persistence (502/504).

**Integration tests** (build tag `//go:build integration`):
- `httptest.NewServer` real con el router completo + fakes downstream (extractor y persistencia) también en `httptest`: valida el **end-to-end en memoria** (sin disco): subir → validar → reenviar multipart al fake extractor → persistir → 200 PROCESSED; y el caso REUSED con estado precargado.
- Escenario de timeout: fake downstream que duerme más que `EXTRACTOR_TIMEOUT` ⇒ 504.
- Comprobación de que **nunca** se escribe a disco en el flujo (no hay `os.CreateTemp`/S3 en el código de producción; test auxiliar de paquete).

**Comandos** (definidos en Makefile):
- Build: `go build ./...`
- Test: `go test ./...`
- Race: `go test -race ./...`
- Lint: `go vet ./...` (+ `staticcheck ./...` recomendado)
- Integración: `go test -tags=integration ./...`

**Dependencias** (`go.mod`): `github.com/go-chi/chi/v5` (v5), `github.com/pdfcpu/pdfcpu` (última estable), stdlib. Mocks manuales ⇒ sin dependencia de mock en runtime. Go ≥ 1.24.

## Task List

El desglose completo con criterios de aceptación, verificación y dependencias está en
`tasks/todo.md`. Resumen (orden de construcción topológico, grants verticales):

**Phase 0 — Bootstrap**
- Task 1: `go.mod`, esqueleto de directorios, Makefile (build/test/race/vet). Depende: —.
- Task 2: `platform/config` (env) + `platform/problem` (RFC 9457) + tests. Depende: 1.

**Phase 1 — Validación (fail fast temprano)**
- Task 3: `platform/pdf` wrapper pdfcpu (estructura + cifrado) + fixtures PDF testdata. Depende: 2.
- Task 4: `handler` streaming multipart + magic bytes + límites (400/413/422 problem+json). Depende: 2.
- **Checkpoint A**: `go test ./...` verde; subir un JPG y un PDF real producen 422 y 200 (revisión humana).

**Phase 2 — Downstream y orquestación**
- Task 5: `client` extractor (io.Pipe + ContentLength) y persistence (FindByChecksum/Store) + fakes httptest. Depende: 3.
- Task 6: `service/orchestrator` (dedup→validate→extract→store, race 409) + mocks. Depende: 4, 5.
- **Checkpoint B**: orquestador con mocks verde; dedup hit no invoca al extractor (review).

**Phase 3 — Wiring y E2E**
- Task 7: `router.go` (chi) + middleware (request-id/recoverer/semáforo) + `cmd/orchestrator/main.go` + graceful shutdown. Depende: 6.
- Task 8: integration tests end-to-end (sin disco), xs volcado de `api/openapi.yaml` lint + README de operación. Depende: 7.
- **Checkpoint C**: E2E PROCESSED/REUSED/timeout, sin escritura a disco; revisión humana previa a cualquier deploy.

## Risks and Mitigations

| Riesgo | Impacto | Mitigación |
|---|---|---|
| Cifrado: API exacta de pdfcpu para detectar (sentinel vs string) | Med | Task 3 fija versión en go.mod y congela el mecanismo con test dedicado |
| `DecodeAllStreams` default de pdfcpu puede inflar memoria | Med | `conf.DecodeAllStreams=false` + semáforo `MaxConcurrency`; medir con `-race`/pprof en E2E |
| Race de dedup: coexisten 2 request con el mismo checksum | Med | Persistencia DEBE dar unicidad en `checksum` (constraint único); 409 → orquestador relee y responde REUSED (reclamo en Open Questions/contrato downstream) |
| Extractor no acepta `Content-Length` (multipart) | Bajo | `ContentLength` precalculado es estándar HTTP; fallback a chunked si el test del fake lo exige |
| Error downstream con body problem+json propio | Bajo | `client/transport.go` traduce; nunca se re-expone el body crudo del tercero |
| Fuga de memoria por buffer retenido | Bajo | `defer` de cierre en handler; buffer vive solo el scope del request |
| Cambios de contrato de Persistencia (endpoints dedup) | Alto | **Resuelto (Open Questions 1 y 2):** `GET /documents/by-checksum/{sha256}` + `POST /documents` con 409 confirmados; body plano `StoreDocumentRequest` tipado, sin passthrough |

## Open Questions

**Resueltas (esta iteración):**

1. ✔ **Persistencia expone** `GET /api/v1/documents/by-checksum/{checksum}`, `POST /api/v1/documents`
   y responde **409** ante checksum duplicado (unicidad). Contrato del modelo de dedup confirmado.
2. ✔ Sin passthrough genérico: Persistencia recibe **`StoreDocumentRequest`** con esquema plano
   estricto (compatible con `PdfDocument` de PaperSoul). `json.RawMessage` queda **descartado**.
3. ✔ (cerrada, no aplica) La respuesta 200 es `documentId + status + checksum + metadata`, **sin**
   el texto extraído crudo (se optimiza el ancho de banda; el texto queda en Persistencia).
4. ✔ 25 MiB confirmado como tope máximo de subida.

**Pendientes:**

1. ¿Auth/rate-limit entre microservicios en el deploy real? Fuera de alcance de esta fase.
2. ¿Necesidad de `pdfcpu Create` para generar el fixture "PDF válido" en tests (evitar binarios
   commiteados) o se commitea un fixture estático en `testdata/`?

## Verification (previo a implementar)

- [ ] `api/openapi.yaml` revisado y aprobado por el usuario (contrato Fase 1).
- [ ] Las 4 decisiones de contrato quedaron escritas (Overview) y validadas.
- [ ] Cada Task en `tasks/todo.md` tiene criterios de aceptación + verificación + dependencias.
- [ ] Carpetas `tasks/` y `api/` versionadas; `tasks/plan.md` y `tasks/todo.md` no heredan trabajo incompleto de otro plan (repo inicial, sin conflictos).
- [ ] Revisión humana de esta Fase 2 antes del primer commit de código.