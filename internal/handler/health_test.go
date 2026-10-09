package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/problem"
)

// TestHealthDevuelve200SinTocarElServicio fija la semántica de liveness: el
// endpoint responde por el proceso HTTP y no depende de la configuración ni de
// los downstream, así que el servicio no debe recibir ninguna llamada.
func TestHealthDevuelve200SinTocarElServicio(t *testing.T) {
	svc := &fakeService{}
	rr := doRouter(newTestRouter(svc, testConfig()),
		httptest.NewRequest(http.MethodGet, handler.HealthPath, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200 (body %q)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, quiero application/json", ct)
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decodificando el body %q: %v", rr.Body.String(), err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, quiero ok", got.Status)
	}
	if svc.calls != 0 {
		t.Errorf("el servicio recibió %d llamadas: el liveness no debe tocar downstream", svc.calls)
	}
}

func TestHealthConMetodoNoPermitidoDevuelve405ConAllowGet(t *testing.T) {
	rr := doRouter(newTestRouter(&fakeService{}, testConfig()),
		httptest.NewRequest(http.MethodPost, handler.HealthPath, nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, quiero 405 (body %q)", rr.Code, rr.Body.String())
	}
	if allow := rr.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, quiero que incluya GET", allow)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, quiero application/problem+json", ct)
	}
	p := decodeProblem(t, rr)
	if p.Type != problem.TypeMethodNotAllowed || p.Status != http.StatusMethodNotAllowed {
		t.Errorf("problem = %+v, quiero type=%s status=405", p, problem.TypeMethodNotAllowed)
	}
}

// TestHealthNoLoAfectaLaSaturacionDeConcurrencia prueba la excepción del
// liveness frente a MaxConcurrency: con el único slot ocupado, /process da 503
// pero /health sigue respondiendo 200 porque no compite por capacidad de negocio.
func TestHealthNoLoAfectaLaSaturacionDeConcurrencia(t *testing.T) {
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

	primer := httptest.NewRecorder()
	primerListo := make(chan struct{})
	go func() {
		defer close(primerListo)
		h.ServeHTTP(primer, newRequest(t, true, validPdfBytes(64), nil))
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("el primer request nunca entró al servicio")
	}

	if rr := doRouter(h, newRequest(t, true, validPdfBytes(64), nil)); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("segundo /process = %d, quiero 503 con el semáforo saturado", rr.Code)
	}
	if rr := doRouter(h, httptest.NewRequest(http.MethodGet, handler.HealthPath, nil)); rr.Code != http.StatusOK {
		t.Errorf("GET /health = %d, quiero 200 aunque el semáforo esté saturado (body %q)", rr.Code, rr.Body.String())
	}

	close(release)
	<-primerListo
}
