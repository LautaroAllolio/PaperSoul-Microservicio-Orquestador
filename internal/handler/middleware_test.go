package handler_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
)

func loggerTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func TestRecovererConvierteElPanicEnProblem500YLoLoguea(t *testing.T) {
	const secreto = "secreto: token=abc123"
	var logBuf bytes.Buffer
	mw := handler.RequestID(loggerTo(&logBuf))(
		handler.Recoverer(loggerTo(&logBuf))(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic(secreto)
		})))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", nil)
	req.Header.Set("X-Correlation-Id", testCorrelationID)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, quiero application/problem+json", ct)
	}
	if strings.Contains(rr.Body.String(), secreto) {
		t.Errorf("el body filtró el panic: %s", rr.Body.String())
	}
	p := decodeProblem(t, rr)
	if p.Detail != "" {
		t.Errorf("detail = %q, quiero vacío", p.Detail)
	}
	if p.Instance != "urn:uuid:"+testCorrelationID {
		t.Errorf("instance = %q, quiero el correlation id del request", p.Instance)
	}
	// El stack va al log del servidor, no al cliente.
	if logged := logBuf.String(); !strings.Contains(logged, secreto) {
		t.Errorf("el panic no quedó registrado en el log: %s", logged)
	}
}

func TestRecovererNoReescribeSiLaRespuestaYaEmpezo(t *testing.T) {
	var logBuf bytes.Buffer
	mw := handler.Recoverer(loggerTo(&logBuf))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"documentId":"doc-1"`)) // respuesta parcial
		panic("boom tardío")
	}))

	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200: la respuesta ya había empezado", rr.Code)
	}
	if body := rr.Body.String(); body != `{"documentId":"doc-1"` {
		t.Fatalf("body = %q, quiero la respuesta parcial intacta (no un problem 500 encima)", body)
	}
}

func TestRequestLoggerEscribeMetodoPathStatusDuracionYCorrelationId(t *testing.T) {
	var logBuf bytes.Buffer
	mw := handler.RequestID(loggerTo(&logBuf))(
		handler.RequestLogger(loggerTo(&logBuf))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", nil)
	req.Header.Set("X-Correlation-Id", testCorrelationID)
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Fatalf("status = %d, quiero 418: el logger no debe alterar la respuesta", rr.Code)
	}
	got := logBuf.String()
	for _, want := range []string{
		"method=POST",
		"path=/api/v1/documents/process",
		"status=418",
		"correlation_id=" + testCorrelationID,
		"duration_ms=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("el log %q no contiene %q", got, want)
		}
	}
}

func TestRequestLoggerRegistraEl500DeUnPanic(t *testing.T) {
	// Fija el orden de la cadena: si el recoverer estuviera por fuera del logger,
	// el panic escaparía de la línea de log y se registraría como status=200.
	var logBuf bytes.Buffer
	mw := handler.RequestID(loggerTo(&logBuf))(
		handler.RequestLogger(loggerTo(&logBuf))(
			handler.Recoverer(loggerTo(&logBuf))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("boom")
			}))))

	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/documents/process", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	if got := logBuf.String(); !strings.Contains(got, "status=500") {
		t.Errorf("el log %q no registra el 500 del panic", got)
	}
}

func TestElSemaforoLiberaElSlotAlTerminarCadaRequest(t *testing.T) {
	var llamadas int
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		llamadas++
		return &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc"}, nil
	}}
	h := newTestRouter(svc, testConfig()) // MaxConcurrency: 1

	for i := 0; i < 5; i++ {
		rr := doRouter(h, newRequest(t, true, validPdfBytes(64), nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d = %d, quiero 200: el semáforo no liberó el slot (body=%s)", i+1, rr.Code, rr.Body.String())
		}
	}
	if llamadas != 5 {
		t.Errorf("el servicio recibió %d requests, quiero 5", llamadas)
	}
}

func TestElSemaforoSueltaElSlotAunqueElHandlerEntreEnPanic(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrency = 1
	var llamadas int
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		llamadas++
		if llamadas == 1 {
			panic("boom")
		}
		return &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc"}, nil
	}}
	h := newTestRouter(svc, cfg)

	if rr := doRouter(h, newRequest(t, true, validPdfBytes(64), nil)); rr.Code != http.StatusInternalServerError {
		t.Fatalf("primer request = %d, quiero 500", rr.Code)
	}
	segundo := doRouter(h, newRequest(t, true, validPdfBytes(64), nil))
	if segundo.Code != http.StatusOK {
		t.Fatalf("segundo request = %d, quiero 200: el slot quedó tomado tras el panic (body=%s)", segundo.Code, segundo.Body.String())
	}
}
