//go:build integration

// Tests de integración end-to-end, con build tag `integration` (make
// integration). Montan el cableado real de cmd/orchestrator/main.go — router
// chi, orquestador, clients HTTP y validador pdfcpu — contra fakes downstream
// también por HTTP.
//
// El valor de estos tests no está en cubrir el flujo (eso lo hacen los unitarios
// por capa, con fakes en memoria): está en ejercitar las costuras que solo
// existen cuando todo está en un proceso. El body real pasando por el socket, el
// multipart que arma mime/multipart, el ContentLength precalculado, el
// correlation id viajando por el contexto hasta los downstream y el pdfcpu
// validando un binario que nadie le pasa a mano.
package handler_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	"github.com/papersoul/orchestrator/internal/platform/pdf"
	"github.com/papersoul/orchestrator/internal/platform/problem"
	"github.com/papersoul/orchestrator/internal/service"
)

const (
	// fixturePDF es el PDF válido mínimo y estático que ya usa platform/pdf
	// (xref con offsets exactos). Se lee de disco porque es un test: la
	// prohibición de escribir a disco es para el código de producción, y
	// reutilizar el fixture evita mantener un segundo PDF válido en el repo.
	fixturePDF = "../platform/pdf/testdata/valid.pdf"

	nombreEnVuelo = "factura.pdf"
	// textoExtraido es lo que devuelve el fake del Extractor; el text_hash que
	// espera Persistencia es su SHA-256.
	textoExtraido = "PapelSoul: texto extraído del PDF de prueba."
	// pageCountDelFake no coincide con el pageCount real del fixture a propósito:
	// el definitive es el que reporta el Extractor, no el validador.
	pageCountDelFake = 7
	// mediaProblem seliteral a propósito en vez de usar una constante del código:
	// el test debe fallar si el borde cambia el media type, no validar la
	// constante contra sí misma.
	mediaProblem = "application/problem+json"
)

// --- harness ---------------------------------------------------------------

type e2e struct {
	srv *httptest.Server
	ext *fakeExtractor
	per *fakePersistence
	cfg config.Config
}

// montarE2E replica el wiring de cmd/orchestrator/main.go: config, clients
// downstream reales apuntando a los fakes, orquestador con el validador pdfcpu
// de verdad y el router chi completo.
func montarE2E(t *testing.T, ajustar func(*config.Config)) *e2e {
	t.Helper()

	cfg := config.Config{
		Addr:               ":0",
		MaxFileSize:        25 << 20,
		MaxBodyBytes:       (25 << 20) + 65536,
		ValidationRelaxed:  true,
		MaxConcurrency:     8,
		ExtractorTimeout:   30 * time.Second,
		PersistenceTimeout: 15 * time.Second,
		ShutdownTimeout:    10 * time.Second,
	}
	ext := &fakeExtractor{respuesta: domain.ExtractResponse{
		ExtractedText:    textoExtraido,
		ExtractionMethod: domain.ExtractionMethodPyMuPDF,
		PageCount:        pageCountDelFake,
	}}
	per := newFakePersistence()

	extSrv := httptest.NewServer(ext.handler())
	t.Cleanup(extSrv.Close)
	perSrv := httptest.NewServer(per.handler())
	t.Cleanup(perSrv.Close)

	cfg.ExtractorURL = extSrv.URL
	cfg.PersistenceURL = perSrv.URL
	if ajustar != nil {
		ajustar(&cfg)
	}

	orchestrator := service.NewOrchestrator(
		client.NewExtractor(client.Options{BaseURL: cfg.ExtractorURL, Timeout: cfg.ExtractorTimeout}),
		client.NewPersistence(client.Options{BaseURL: cfg.PersistenceURL, Timeout: cfg.PersistenceTimeout}),
		pdf.New(cfg.ValidationRelaxed),
	)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(handler.NewRouter(orchestrator, cfg, logger))
	t.Cleanup(srv.Close)

	return &e2e{srv: srv, ext: ext, per: per, cfg: cfg}
}

func leerFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePDF)
	if err != nil {
		t.Fatalf("leyendo el fixture %s: %v", fixturePDF, err)
	}
	return b
}

func checksumDe(b []byte) string {
	suma := sha256.Sum256(b)
	return hex.EncodeToString(suma[:])
}

// respuestaHTTP es la respuesta del orquestador ya leída entera: los tests la
// decodifican como proceso o como problem según el status esperado.
type respuestaHTTP struct {
	status        int
	header        http.Header
	body          []byte
	correlationID string
}

func (r respuestaHTTP) decodificar(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("decodificando la respuesta (body %q): %v", r.body, err)
	}
}

func (r respuestaHTTP) problem(t *testing.T) problem.Problem {
	t.Helper()
	if ct := r.header.Get("Content-Type"); ct != mediaProblem {
		t.Fatalf("Content-Type = %q, quiero %q", ct, mediaProblem)
	}
	var p problem.Problem
	r.decodificar(t, &p)
	return p
}

// metadataProceso replica el shape del 200 (handler.processResponse), que es
// privado del paquete handler.
type metadataProceso struct {
	FileName  string `json:"fileName"`
	SizeBytes int64  `json:"sizeBytes"`
	PageCount int    `json:"pageCount"`
	Encrypted bool   `json:"encrypted"`
}

type procesoProceso struct {
	DocumentID string          `json:"documentId"`
	Status     string          `json:"status"`
	Checksum   string          `json:"checksum"`
	Metadata   metadataProceso `json:"metadata"`
}

// subirPDF manda el fixture como campo `file` de un multipart por un client
// real: el body cruza el socket, con el Content-Type y el Content-Length que
// arma el cliente.
func (e *e2e) subirPDF(t *testing.T, nombre string, contenido []byte, correlationID string) respuestaHTTP {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	parte, err := mw.CreateFormFile("file", nombre)
	if err != nil {
		t.Fatalf("creando el file part: %v", err)
	}
	if _, err := parte.Write(contenido); err != nil {
		t.Fatalf("escribiendo el file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("cerrando el multipart: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.srv.URL+handler.ProcessPath, &body)
	if err != nil {
		t.Fatalf("armando el request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if correlationID != "" {
		req.Header.Set("X-Correlation-Id", correlationID)
	}

	// Timeout holgado: el que decide el deadline del downstream es el server
	// bajo prueba, no este cliente.
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("subiendo el PDF: %v", err)
	}
	defer res.Body.Close()
	crudo, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("leyendo la respuesta: %v", err)
	}
	return respuestaHTTP{
		status:        res.StatusCode,
		header:        res.Header,
		body:          crudo,
		correlationID: res.Header.Get("X-Correlation-Id"),
	}
}

// --- escenarios ------------------------------------------------------------

// TestE2EProcesaUnPDFNuevoYLoPersiste es el camino completo: entra el binario
// por el borde, no existe el checksum, se valida, se extrae y se persiste.
func TestE2EProcesaUnPDFNuevoYLoPersiste(t *testing.T) {
	binario := leerFixture(t)
	checksum := checksumDe(binario)
	h := montarE2E(t, nil)

	res := h.subirPDF(t, nombreEnVuelo, binario, testCorrelationID)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, quiero 200 (body %s)", res.status, res.body)
	}
	if res.correlationID != testCorrelationID {
		t.Errorf("X-Correlation-Id de respuesta = %q, quiero el enviado (%q)", res.correlationID, testCorrelationID)
	}

	cuerpo := procesoProceso{}
	res.decodificar(t, &cuerpo)
	if cuerpo.Status != string(domain.StatusProcessed) {
		t.Errorf("status = %q, quiero PROCESSED", cuerpo.Status)
	}
	if cuerpo.Checksum != checksum {
		t.Errorf("checksum = %q, quiero el sha256 del binario (%q)", cuerpo.Checksum, checksum)
	}
	if cuerpo.DocumentID == "" {
		t.Error("documentId viene vacío")
	}
	meta := cuerpo.Metadata
	if meta.FileName != nombreEnVuelo {
		t.Errorf("metadata.fileName = %q, quiero %q", meta.FileName, nombreEnVuelo)
	}
	if meta.SizeBytes != int64(len(binario)) {
		t.Errorf("metadata.sizeBytes = %d, quiero %d", meta.SizeBytes, len(binario))
	}
	if meta.PageCount != pageCountDelFake {
		t.Errorf("metadata.pageCount = %d, quiero %d (el del extractor, no el del validador)", meta.PageCount, pageCountDelFake)
	}
	if meta.Encrypted {
		t.Error("metadata.encrypted = true: el orquestador acepta un PDF y lo declara cifrado")
	}

	// El Extractor recibió el binario crudo, íntegro y con el checksum de
	// referencia; el ContentLength exacto prueba que no fue transfer-chunked.
	llamadas := h.ext.llamadas()
	if len(llamadas) != 1 {
		t.Fatalf("el Extractor recibió %d requests, quiero 1", len(llamadas))
	}
	got := llamadas[0]
	if err := h.ext.err(); err != nil {
		t.Fatalf("el fake del Extractor no pudo leer la request: %v", err)
	}
	if got.metodo != http.MethodPost || got.path != "/api/v1/extractions" {
		t.Errorf("el Extractor recibió %s %s, quiero POST /api/v1/extractions", got.metodo, got.path)
	}
	if !strings.HasPrefix(got.contentType, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q, quiero multipart/form-data con boundary", got.contentType)
	}
	if !bytes.Equal(got.binario, binario) {
		t.Errorf("el binario que llegó al Extractor difiere del enviado (%d vs %d bytes)", len(got.binario), len(binario))
	}
	if got.filename != nombreEnVuelo {
		t.Errorf("la parte file venía con filename %q, quiero %q", got.filename, nombreEnVuelo)
	}
	if got.campoChecksum != checksum {
		t.Errorf("el campo checksum del multipart = %q, quiero %q", got.campoChecksum, checksum)
	}
	if got.checksumHeader != checksum {
		t.Errorf("X-Document-Checksum = %q, quiero %q", got.checksumHeader, checksum)
	}
	if got.correlationID != testCorrelationID {
		t.Errorf("el Extractor recibió X-Correlation-Id %q, quiero %q (el enviado por el cliente)", got.correlationID, testCorrelationID)
	}
	if got.contentLength <= 0 {
		t.Errorf("ContentLength = %d: la request del Extractor no lo lleva, eso fuerza chunked", got.contentLength)
	}
	if got.contentLength != got.bytesLeidos {
		t.Errorf("ContentLength = %d pero el body tenía %d bytes: el precalculado no calza", got.contentLength, got.bytesLeidos)
	}

	// Persistencia: primero consulta por checksum (miss) y después da de alta.
	altas := h.per.altas()
	if len(altas) != 1 {
		t.Fatalf("Persistencia recibió %d altas, quiero 1", len(altas))
	}
	alta := altas[0]
	if alta.PDFHash != checksum {
		t.Errorf("StoreDocumentRequest.pdf_hash = %q, quiero el checksum %q", alta.PDFHash, checksum)
	}
	if alta.FileName != nombreEnVuelo {
		t.Errorf("StoreDocumentRequest.filename = %q, quiero %q", alta.FileName, nombreEnVuelo)
	}
	if alta.ExtractedText != textoExtraido {
		t.Errorf("StoreDocumentRequest.extracted_text = %q, quiero el texto del Extractor", alta.ExtractedText)
	}
	if alta.ExtractionMethod != domain.ExtractionMethodPyMuPDF {
		t.Errorf("StoreDocumentRequest.extraction_method = %q, quiero pymupdf", alta.ExtractionMethod)
	}
	if alta.PageCount != pageCountDelFake {
		t.Errorf("StoreDocumentRequest.page_count = %d, quiero %d", alta.PageCount, pageCountDelFake)
	}
	if alta.TextHash != checksumDe([]byte(textoExtraido)) {
		t.Errorf("StoreDocumentRequest.text_hash = %q, quiero el sha256 del texto extraído", alta.TextHash)
	}
	if alta.UploadedAt.IsZero() {
		t.Error("StoreDocumentRequest.uploaded_at vino en cero")
	}
	if delta := time.Since(alta.UploadedAt); delta < -time.Second || delta > time.Minute {
		t.Errorf("StoreDocumentRequest.uploaded_at = %s, fuera de la ventana razonable (delta %s)", alta.UploadedAt, delta)
	}
	if h.per.busquedas() != 1 {
		t.Errorf("Persistencia recibió %d consultas por checksum, quiero 1 (el dedup)", h.per.busquedas())
	}
}

// TestE2EReutilizaUnDocumentoYaPersistido cubre el dedup: el checksum ya existe,
// así que no se invoca al Extractor y la respuesta sale de Persistencia.
func TestE2EReutilizaUnDocumentoYaPersistido(t *testing.T) {
	binario := leerFixture(t)
	checksum := checksumDe(binario)
	precargado := domain.StoredDocumentResponse{
		ID:        "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
		Status:    "STORED",
		PDFHash:   checksum,
		FileName:  "factura-del-mes-anterior.pdf",
		PageCount: 42,
	}
	h := montarE2E(t, nil)
	h.per.precargar(checksum, precargado)

	res := h.subirPDF(t, nombreEnVuelo, binario, testCorrelationID)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, quiero 200 (body %s)", res.status, res.body)
	}

	cuerpo := procesoProceso{}
	res.decodificar(t, &cuerpo)
	if cuerpo.Status != string(domain.StatusReused) {
		t.Errorf("status = %q, quiero REUSED", cuerpo.Status)
	}
	if cuerpo.DocumentID != precargado.ID {
		t.Errorf("documentId = %q, quiero el persistido %q", cuerpo.DocumentID, precargado.ID)
	}
	if cuerpo.Checksum != checksum {
		t.Errorf("checksum = %q, quiero %q", cuerpo.Checksum, checksum)
	}
	if cuerpo.Metadata.FileName != precargado.FileName || cuerpo.Metadata.PageCount != precargado.PageCount {
		t.Errorf("metadata = %+v, quiero la del documento persistido (%q, %d páginas)",
			cuerpo.Metadata, precargado.FileName, precargado.PageCount)
	}
	if cuerpo.Metadata.SizeBytes != int64(len(binario)) {
		t.Errorf("metadata.sizeBytes = %d, quiero el tamaño del upload %d", cuerpo.Metadata.SizeBytes, len(binario))
	}

	// El criterio de la deduplicación: el dedup hit no gasta extracción.
	if n := h.ext.total(); n != 0 {
		t.Errorf("el Extractor recibió %d requests: con el checksum ya persistido no debe extraerse", n)
	}
	if n := len(h.per.altas()); n != 0 {
		t.Errorf("Persistencia recibió %d altas, quiero 0: el documento ya existía", n)
	}
}

// TestE2EDevuelve504CuandoElExtractorNoResponde mapea el deadline del client
// downstream a 504 problem+json, sin dejar nada a medias en Persistencia.
func TestE2EDevuelve504CuandoElExtractorNoResponde(t *testing.T) {
	binario := leerFixture(t)
	h := montarE2E(t, func(c *config.Config) {
		c.ExtractorTimeout = 150 * time.Millisecond
	})
	// Más lento que el timeout del client: el handler del fake sigue dormido
	// cuando el cliente ya cortó, y httptest.Server.Close() espera a que
	// termine, así que este test paga el sleep entero.
	h.ext.dormirPor = 600 * time.Millisecond

	res := h.subirPDF(t, nombreEnVuelo, binario, testCorrelationID)
	if res.status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, quiero 504 (body %s)", res.status, res.body)
	}
	p := res.problem(t)
	if p.Type != problem.TypeDownstreamTimeout {
		t.Errorf("problem.type = %q, quiero %q", p.Type, problem.TypeDownstreamTimeout)
	}
	if p.Status != http.StatusGatewayTimeout {
		t.Errorf("problem.status = %d, quiero 504", p.Status)
	}
	if p.Instance != "urn:uuid:"+testCorrelationID {
		t.Errorf("problem.instance = %q, quiero el correlation id del request", p.Instance)
	}
	if h.per.totalAltas() != 0 {
		t.Errorf("Persistencia recibió %d altas: sin extracción no hay nada que persistir", h.per.totalAltas())
	}
}

// TestE2EDevuelve413SiElBinarioExcedeElLimite prueba el cap del archivo a
// través del stack real (MaxBytesReader + LimitReader), no con un recorder.
func TestE2EDevuelve413SiElBinarioExcedeElLimite(t *testing.T) {
	binario := leerFixture(t)
	h := montarE2E(t, func(c *config.Config) {
		c.MaxFileSize = 64 // el fixture es mayor a propósito
		c.MaxBodyBytes = 64 + 1024
	})

	res := h.subirPDF(t, nombreEnVuelo, binario, testCorrelationID)
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, quiero 413 (body %s)", res.status, res.body)
	}
	p := res.problem(t)
	if p.Type != problem.TypeFileTooLarge {
		t.Errorf("problem.type = %q, quiero %q", p.Type, problem.TypeFileTooLarge)
	}
	// El 413 es fail-fast: no se consultó ni se extrajo nada.
	if h.ext.total() != 0 || h.per.busquedas() != 0 {
		t.Errorf("el 413 llegó a tocar los downstream (extractor=%d, consultas=%d)", h.ext.total(), h.per.busquedas())
	}
}

// TestE2EHealthSobreviveALaCaidaDeLosDownstream fija que el liveness no depende
// de Extracción ni Persistencia: con ambos caídos, /health responde 200 mientras
// el procesamiento real devuelve 502.
func TestE2EHealthSobreviveALaCaidaDeLosDownstream(t *testing.T) {
	binario := leerFixture(t)
	h := montarE2E(t, func(c *config.Config) {
		c.ExtractorURL = "http://127.0.0.1:1"
		c.PersistenceURL = "http://127.0.0.1:1"
	})

	res, err := http.Get(h.srv.URL + handler.HealthPath)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer res.Body.Close()
	cuerpo, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("leyendo /health: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /health = %d, quiero 200 con los downstream caídos (body %q)", res.StatusCode, cuerpo)
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cuerpo, &got); err != nil || got.Status != "ok" {
		t.Errorf("body de /health = %q, quiero {\"status\":\"ok\"}", cuerpo)
	}

	proceso := h.subirPDF(t, nombreEnVuelo, binario, testCorrelationID)
	if proceso.status != http.StatusBadGateway {
		t.Fatalf("POST /process = %d, quiero 502 con los downstream caídos (body %s)", proceso.status, proceso.body)
	}
}

// funcionesDeDisco son los métodos de os/ioutil que crean o borran archivos.
// Se comparan por nombre exacto sobre el selector, no por substring: un grep
// pegaría con los comentarios que documentan la regla y con la palabra
// "dominio" (que contiene "minio"). Un guard que llora lobo lo desactivan.
var funcionesDeDisco = map[string]bool{
	"Create": true, "CreateTemp": true, "MkdirTemp": true, "MkdirAll": true,
	"WriteFile": true, "OpenFile": true, "Remove": true, "RemoveAll": true, "Rename": true,
	"TempFile": true,
}

// llamadasQueVuelcanADisco son las de net/http:.ParseMultipartForm y
// FormFile escriben el body a tempfiles. Se buscan por nombre sobre cualquier
// receptor, así que CreateFormFile (el writer de mime/multipart, que no toca
// el disco) no entra en la lista.
var llamadasQueVuelcanADisco = map[string]bool{
	"ParseMultipartForm": true,
	"FormFile":           true,
}

// paquetesDeObjetos son prefijos de import path de SDKs de almacenamiento de
// objetos: están fuera por decisión de diseño, no por falta de uso hoy.
var paquetesDeObjetos = []string{
	"github.com/minio/", "github.com/aws/", "gocloud.dev/blob", "cloud.google.com/go/storage",
}

// TestE2ENuncaEscribeADisco es el criterio de "cero almacenamiento
// intermedio" verificado por escaneo: ninguna ruta de producción del binario
// crea archivos, directorios ni habla de S3/MinIO.
//
// El escaneo es estático a propósito —no una introspección en runtime— porque
// lo que prohíbe la arquitectura es que exista la llamada en el código, no que
// se ejecute hoy. Se parsea con go/ast en vez de con grep para no contar
// comentarios ni cadenas como usos.
func TestE2ENuncaEscribeADisco(t *testing.T) {
	raiz := raizDelModulo(t)
	var revisados int
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(raiz, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Los _test.go quedan afuera: los tests leen fixtures y escriben en
			// t.TempDir, y no son rutas de producción.
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			revisados++
			revisarArchivoSinDisco(t, raiz, path)
			return nil
		})
		if err != nil {
			t.Fatalf("recorriendo %s: %v", dir, err)
		}
	}
	if revisados == 0 {
		t.Fatal("no se revisó ningún archivo de producción: el escaneo no está mirando el lugar correcto")
	}
	t.Logf("escaneados %d archivos de producción", revisados)

	// go.mod también: un SDK de S3 como dependencia arrastra la capacidad de
	// escribir a disco aunque hoy nadie lo llame.
	gomod, err := os.ReadFile(filepath.Join(raiz, "go.mod"))
	if err != nil {
		t.Fatalf("leyendo go.mod: %v", err)
	}
	patronModulo := regexp.MustCompile(`(minio|aws-sdk|gocloud\.dev/blob|cloud\.google\.com/go/storage)`)
	for i, linea := range strings.Split(string(gomod), "\n") {
		if uso := patronModulo.FindString(linea); uso != "" {
			t.Errorf("go.mod:%d declara %q: el orquestador no usa almacenamiento de objetos", i+1, uso)
		}
	}
}

func revisarArchivoSinDisco(t *testing.T, raiz, path string) {
	t.Helper()
	rel, _ := filepath.Rel(raiz, path)
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", rel, err)
	}

	reportar := func(nodo ast.Node, uso string) {
		pos := fset.Position(nodo.Pos())
		t.Errorf("%s:%d usa %q: el orquestador no escribe a disco ni usa S3/MinIO",
			rel, pos.Line, uso)
	}

	ast.Inspect(archivo, func(nodo ast.Node) bool {
		switch n := nodo.(type) {
		case *ast.ImportSpec:
			paquete := strings.Trim(n.Path.Value, `"`)
			for _, prefijo := range paquetesDeObjetos {
				if strings.HasPrefix(paquete, prefijo) {
					reportar(n, paquete)
				}
			}
		case *ast.SelectorExpr:
			// os.Create / ioutil.TempFile: el receptor tiene que ser el
			// paquete, no cualquier variable que se llame os.
			receptor, ok := n.X.(*ast.Ident)
			if !ok || (receptor.Name != "os" && receptor.Name != "ioutil") {
				return true
			}
			if funcionesDeDisco[n.Sel.Name] {
				reportar(n, receptor.Name+"."+n.Sel.Name)
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if llamadasQueVuelcanADisco[sel.Sel.Name] {
				reportar(n, sel.Sel.Name)
			}
		}
		return true
	})
}

// raizDelModulo sube desde este archivo hasta el directorio con go.mod, para
// que el escaneo no dependa del directorio de trabajo de `go test`.
func raizDelModulo(t *testing.T) string {
	t.Helper()
	_, archivo, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no pude ubicar el archivo de test")
	}
	dir := filepath.Dir(archivo)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		padre := filepath.Dir(dir)
		if padre == dir {
			t.Fatalf("no encontré go.mod subiendo desde %s", filepath.Dir(archivo))
		}
		dir = padre
	}
}

// --- fakes de los downstream -----------------------------------------------

type llamadaExtractor struct {
	metodo         string
	path           string
	contentType    string
	contentLength  int64
	bytesLeidos    int64
	checksumHeader string
	correlationID  string
	filename       string
	campoChecksum  string
	binario        []byte
}

// fakeExtractor simula el microservicio de Extracción. Registra lo que recibió
// para que el test verifique el formato real de la request, y puede dormirse
// para provocar el timeout del client.
type fakeExtractor struct {
	mu        sync.Mutex
	registros []llamadaExtractor
	fallo     error

	respuesta domain.ExtractResponse
	dormirPor time.Duration
}

func (f *fakeExtractor) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/extractions", func(w http.ResponseWriter, r *http.Request) {
		if d := f.dormirPor; d > 0 {
			// Cortar en cuanto el cliente aborta: si no, httptest.Server.Close
			// esperaría los 2s del sleep de cada test.
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		f.registrar(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.respuesta)
	})
	return mux
}

func (f *fakeExtractor) registrar(r *http.Request) {
	l := llamadaExtractor{
		metodo:         r.Method,
		path:           r.URL.Path,
		contentType:    r.Header.Get("Content-Type"),
		contentLength:  r.ContentLength,
		checksumHeader: r.Header.Get("X-Document-Checksum"),
		correlationID:  r.Header.Get("X-Correlation-Id"),
	}
	defer func() { _ = r.Body.Close() }()

	_, params, err := mime.ParseMediaType(l.contentType)
	if err != nil {
		f.fijarFallo(fmt.Errorf("Content-Type %q no parseable: %w", l.contentType, err))
		return
	}

	// Se leen las partes a mano en vez de usar ReadForm: ReadForm escribe a
	// tempfile lo que no entra en memoria, que es justo lo que este repo
	// prohíbe (aunque acá el archivo es de 329 bytes).
	conteo := &contador{reader: r.Body}
	mr := multipart.NewReader(conteo, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			if err == io.EOF {
				break
			}
			f.fijarFallo(fmt.Errorf("leyendo las partes del multipart: %w", err))
			break
		}
		contenido, err := io.ReadAll(part)
		if err != nil {
			f.fijarFallo(fmt.Errorf("leyendo la parte %q: %w", part.FormName(), err))
			return
		}
		switch part.FormName() {
		case "file":
			l.filename = part.FileName()
			l.binario = contenido
		case "checksum":
			l.campoChecksum = string(contenido)
		}
	}
	l.bytesLeidos = conteo.n

	f.mu.Lock()
	f.registros = append(f.registros, l)
	f.mu.Unlock()
}

func (f *fakeExtractor) fijarFallo(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fallo == nil {
		f.fallo = err
	}
}

func (f *fakeExtractor) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fallo
}

func (f *fakeExtractor) llamadas() []llamadaExtractor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llamadaExtractor(nil), f.registros...)
}

func (f *fakeExtractor) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registros)
}

// contador lleva los bytes leídos del body, para contrastar con el
// ContentLength que declaró el client.
type contador struct {
	reader io.Reader
	n      int64
}

func (c *contador) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.n += int64(n)
	return n, err
}

// fakePersistence simula el microservicio de Persistencia: GET by-checksum
// (404 si no existe) y POST de documentos (409 si el checksum ya está).
type fakePersistence struct {
	mu         sync.Mutex
	documentos map[string]domain.StoredDocumentResponse
	consultas  int
	registros  []domain.StoreDocumentRequest
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{documentos: map[string]domain.StoredDocumentResponse{}}
}

func (p *fakePersistence) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/documents/by-checksum/{checksum}", func(w http.ResponseWriter, r *http.Request) {
		checksum := r.PathValue("checksum")
		p.mu.Lock()
		p.consultas++
		doc, existe := p.documentos[checksum]
		p.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no existe un documento con ese checksum"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("POST /api/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		var in domain.StoreDocumentRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		if _, duplicado := p.documentos[in.PDFHash]; duplicado {
			p.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			return
		}
		doc := domain.StoredDocumentResponse{
			ID:        fmt.Sprintf("doc-%d", len(p.registros)+1),
			Status:    "STORED",
			PDFHash:   in.PDFHash,
			FileName:  in.FileName,
			PageCount: in.PageCount,
		}
		p.documentos[in.PDFHash] = doc
		p.registros = append(p.registros, in)
		p.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(doc)
	})
	return mux
}

func (p *fakePersistence) precargar(checksum string, doc domain.StoredDocumentResponse) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.documentos[checksum] = doc
}

func (p *fakePersistence) altas() []domain.StoreDocumentRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.StoreDocumentRequest(nil), p.registros...)
}

func (p *fakePersistence) totalAltas() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.registros)
}

func (p *fakePersistence) busquedas() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.consultas
}
