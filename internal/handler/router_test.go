package handler_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/problem"
	"github.com/papersoul/orchestrator/internal/platform/reqid"
)

// stubService permite testear el router con comportamientos que fakeService no
// modela (bloqueos, panics) sin tocar el helper compartido por process_test.go.
type stubService struct {
	process func(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error)
}

func (s stubService) Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
	return s.process(ctx, in)
}

func newTestRouter(svc handler.DocumentService, cfg config.Config) http.Handler {
	return handler.NewRouter(svc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func doRouter(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func okService() stubService {
	return stubService{process: func(_ context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
		return &domain.ProcessResult{
			DocumentID: "doc-1",
			Status:     domain.StatusProcessed,
			Checksum:   "abc123",
			FileName:   in.FileName,
			Size:       in.Size,
			PageCount:  3,
		}, nil
	}}
}

func TestRouterHappyPathDevuelve200YReflejaElCorrelationId(t *testing.T) {
	svc := &fakeService{res: &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc123"}}
	rr := doRouter(newTestRouter(svc, testConfig()), newRequest(t, true, validPdfBytes(64), nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200. body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Correlation-Id"); got != testCorrelationID {
		t.Errorf("X-Correlation-Id = %q, quiero %q (el del cliente)", got, testCorrelationID)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, quiero application/json", ct)
	}
}

func TestRouterGeneraCorrelationIdCuandoElClienteNoMandaNinguno(t *testing.T) {
	req := newRequest(t, true, validPdfBytes(64), nil)
	req.Header.Del("X-Correlation-Id")

	rr := doRouter(newTestRouter(okService(), testConfig()), req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200", rr.Code)
	}
	got := rr.Header().Get("X-Correlation-Id")
	if !reqid.Valid(got) {
		t.Fatalf("X-Correlation-Id = %q, quiero un UUID generado por el servidor", got)
	}
}

func TestRouterSustituyeUnCorrelationIdInvalido(t *testing.T) {
	req := newRequest(t, true, validPdfBytes(64), nil)
	req.Header.Set("X-Correlation-Id", "no soy un uuid\r\nX-Injected: 1")

	rr := doRouter(newTestRouter(okService(), testConfig()), req)

	got := rr.Header().Get("X-Correlation-Id")
	if !reqid.Valid(got) {
		t.Fatalf("X-Correlation-Id = %q, el valor inválido del cliente debe reemplazarse", got)
	}
	if injected := rr.Header().Get("X-Injected"); injected != "" {
		t.Errorf("el header del cliente se coló en la respuesta: X-Injected=%q", injected)
	}
}

func TestRouterRutaDesconocidaDevuelve404ProblemJSON(t *testing.T) {
	rr := doRouter(newTestRouter(okService(), testConfig()),
		httptest.NewRequest(http.MethodGet, "/api/v1/documents/inexistente", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, quiero 404", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeNotFound || p.Status != http.StatusNotFound {
		t.Fatalf("problem = %+v, quiero type=%s status=404", p, problem.TypeNotFound)
	}
	if !strings.HasPrefix(p.Instance, "urn:uuid:") || p.Instance == "urn:uuid:" {
		t.Errorf("instance = %q, quiero un urn:uuid con id", p.Instance)
	}
}

func TestRouterMetodoNoPermitidoDevuelve405ConAllow(t *testing.T) {
	rr := doRouter(newTestRouter(okService(), testConfig()),
		httptest.NewRequest(http.MethodGet, "/api/v1/documents/process", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, quiero 405", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
	allow := rr.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow = %q, quiero que incluya POST", allow)
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeMethodNotAllowed || p.Status != http.StatusMethodNotAllowed {
		t.Fatalf("problem = %+v, quiero type=%s status=405", p, problem.TypeMethodNotAllowed)
	}
}

func TestRouterSaturacionDevuelve503ConRetryAfter(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc"}, nil
	}}
	h := newTestRouter(svc, testConfig()) // MaxConcurrency: 1

	primerReq := newRequest(t, true, validPdfBytes(64), nil)
	primer := httptest.NewRecorder()
	primerListo := make(chan struct{})
	go func() {
		defer close(primerListo)
		h.ServeHTTP(primer, primerReq)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("el primer request nunca entró al servicio")
	}

	segundo := doRouter(h, newRequest(t, true, validPdfBytes(64), nil))
	if segundo.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, quiero 503 con el semáforo saturado. body=%s", segundo.Code, segundo.Body.String())
	}
	if got := segundo.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, quiero %q para que el cliente reintente", got, "1")
	}
	if ct := segundo.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, quiero application/problem+json", ct)
	}
	p := decodeProblem(t, segundo)
	if p.Type != problem.TypeOverloaded || p.Status != http.StatusServiceUnavailable {
		t.Errorf("problem = %+v, quiero type=%s status=503", p, problem.TypeOverloaded)
	}

	close(release)
	<-primerListo
	if primer.Code != http.StatusOK {
		t.Fatalf("el primer request = %d, quiero 200: el 503 no debe haberlo afectado", primer.Code)
	}
}

func TestRouterServiceQueEntraEnPanicDevuelve500SinFiltrarElStack(t *testing.T) {
	const secreto = "secreto: token=abc123"
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		panic(secreto)
	}}

	rr := doRouter(newTestRouter(svc, testConfig()), newRequest(t, true, validPdfBytes(64), nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500 ante el panic", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeInternalError || p.Status != http.StatusInternalServerError {
		t.Fatalf("problem = %+v, quiero type=%s status=500", p, problem.TypeInternalError)
	}
	if p.Detail != "" {
		t.Errorf("detail = %q, no debe filtrar nada al cliente", p.Detail)
	}
	if strings.Contains(rr.Body.String(), secreto) {
		t.Errorf("el body filtró el panic: %s", rr.Body.String())
	}
	if p.Instance != "urn:uuid:"+testCorrelationID {
		t.Errorf("instance = %q, quiero el correlation id del request", p.Instance)
	}
}

// TestRouterExponeTodasLasRutasDeErrorComoProblemJSON recorre la matriz de
// errores completa a través del router: cada status sale como
// application/problem+json, con el type correcto y el correlation id del request.
func TestRouterExponeTodasLasRutasDeErrorComoProblemJSON(t *testing.T) {
	desdeElServicio := []struct {
		nombre string
		err    error
		status int
		typ    string
	}{
		{"pdf corrupto", errorsvc.ErrPDFCorrupted, http.StatusUnprocessableEntity, problem.TypeInvalidPDF},
		{"pdf cifrado", errorsvc.ErrPDFEncrypted, http.StatusUnprocessableEntity, problem.TypeEncryptedPDF},
		{"extractor caído", errorsvc.ErrExtractorUnavailable, http.StatusBadGateway, problem.TypeExtractorUnavailable},
		{"respuesta inválida del extractor", errorsvc.ErrExtractorInvalidResponse, http.StatusBadGateway, problem.TypeExtractorUnavailable},
		{"timeout del extractor", errorsvc.ErrExtractorTimeout, http.StatusGatewayTimeout, problem.TypeDownstreamTimeout},
		{"persistencia caída", errorsvc.ErrPersistenceUnavailable, http.StatusBadGateway, problem.TypePersistenceUnavailable},
		{"error interno", errorsvc.ErrInternal, http.StatusInternalServerError, problem.TypeInternalError},
		{"sentinel de control de flujo filtrado", errorsvc.ErrDocumentNotFound, http.StatusInternalServerError, problem.TypeInternalError},
	}

	for _, tc := range desdeElServicio {
		t.Run(tc.nombre, func(t *testing.T) {
			rr := doRouter(newTestRouter(&fakeService{err: tc.err}, testConfig()),
				newRequest(t, true, validPdfBytes(64), nil))
			assertProblemJSON(t, rr, tc.status, tc.typ)
		})
	}

	// Los errores que nacen en el handler (no en el servicio) también deben salir
	// como problem+json: el router no puede cambiarlos.
	desdeElRequest := []struct {
		nombre string
		req    func() *http.Request
		status int
		typ    string
	}{
		{"sin parte file", func() *http.Request { return newRequest(t, false, nil, nil) }, http.StatusBadRequest, problem.TypeInvalidMultipart},
		{"magic bytes inválidos", func() *http.Request { return newRequest(t, true, []byte("no soy un pdf"), nil) }, http.StatusUnprocessableEntity, problem.TypeInvalidPDFHeader},
		{"archivo demasiado grande", func() *http.Request { return newRequest(t, true, validPdfBytes(2048), nil) }, http.StatusRequestEntityTooLarge, problem.TypeFileTooLarge},
		{"ruta desconocida", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/documents/inexistente", nil)
			r.Header.Set("X-Correlation-Id", testCorrelationID)
			return r
		}, http.StatusNotFound, problem.TypeNotFound},
		{"método no permitido", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, handler.ProcessPath, nil)
			r.Header.Set("X-Correlation-Id", testCorrelationID)
			return r
		}, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed},
	}

	for _, tc := range desdeElRequest {
		t.Run(tc.nombre, func(t *testing.T) {
			rr := doRouter(newTestRouter(okService(), testConfig()), tc.req())
			assertProblemJSON(t, rr, tc.status, tc.typ)
		})
	}
}

func assertProblemJSON(t *testing.T, rr *httptest.ResponseRecorder, status int, typ string) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status = %d, quiero %d. body=%s", rr.Code, status, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
	if id := rr.Header().Get("X-Correlation-Id"); id != testCorrelationID {
		t.Errorf("X-Correlation-Id = %q, quiero que se refleje en la respuesta", id)
	}
	p := decodeProblem(t, rr)
	if p.Status != status {
		t.Errorf("problem.status = %d, quiero %d", p.Status, status)
	}
	if p.Type != typ {
		t.Errorf("problem.type = %q, quiero %q", p.Type, typ)
	}
	if p.Instance != "urn:uuid:"+testCorrelationID {
		t.Errorf("problem.instance = %q, quiero el correlation id del request", p.Instance)
	}
}

func TestRouterPropagaElCorrelationIdAlContextoDelServicio(t *testing.T) {
	// El id viaja por el contexto, no por el header: es lo que leen los clients
	// HTTP para reenviarlo a los downstream. Si el middleware solo escribiera el
	// header, la trazabilidad se cortaría en la primera llamada saliente.
	var visto string
	svc := stubService{process: func(ctx context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		visto = reqid.From(ctx)
		return &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc"}, nil
	}}

	rr := doRouter(newTestRouter(svc, testConfig()), newRequest(t, true, validPdfBytes(64), nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200", rr.Code)
	}
	if visto != testCorrelationID {
		t.Errorf("el servicio vio el id %q en el contexto, quiero %q", visto, testCorrelationID)
	}
}

func TestRouterRegistraEl500DeUnPanicEnSuLog(t *testing.T) {
	// El orden de la cadena lo arma NewRouter, no los tests: si el recoverer
	// quedara por fuera del logger, el panic escaparía de la línea de log y se
	// registraría como status=200, dejando el incidente invisible.
	var logBuf bytes.Buffer
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		panic("boom")
	}}
	h := handler.NewRouter(svc, testConfig(), loggerTo(&logBuf))

	rr := doRouter(h, newRequest(t, true, validPdfBytes(64), nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	if got := logBuf.String(); !strings.Contains(got, "status=500") {
		t.Errorf("el log del router %q no registra el 500 del panic", got)
	}
	if got := logBuf.String(); !strings.Contains(got, "correlation_id="+testCorrelationID) {
		t.Errorf("el log del router %q no correlaciona con el request", got)
	}
}

func TestRouterPropagaElErrorDelServicioComoProblemJSON(t *testing.T) {
	svc := &fakeService{err: context.DeadlineExceeded}
	rr := doRouter(newTestRouter(svc, testConfig()), newRequest(t, true, validPdfBytes(64), nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
}
