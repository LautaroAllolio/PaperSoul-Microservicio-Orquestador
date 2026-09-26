package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/domain"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
)

type stubService struct {
	process func(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error)
}

func (s stubService) Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error) {
	return s.process(ctx, in)
}

type httpResult struct {
	res *http.Response
	err error
}

func testConfig() config.Config {
	return config.Config{
		Addr:            "127.0.0.1:0",
		MaxFileSize:     1024,
		MaxBodyBytes:    4096,
		MaxConcurrency:  4,
		ShutdownTimeout: 2 * time.Second,
	}
}

func logDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func processRequest(t *testing.T, baseURL string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormFile("file", "doc.pdf")
	if err != nil {
		t.Fatalf("creando file part: %v", err)
	}
	if _, err := w.Write(append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 64)...)); err != nil {
		t.Fatalf("escribiendo file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("cerrando multipart: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/documents/process", &body)
	if err != nil {
		t.Fatalf("armando request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// startServer levanta el servidor real en un puerto efímero. Es el mismo camino
// que usa run(), salvo que el listener se crea acá para conocer el puerto.
func startServer(t *testing.T, ctx context.Context, srv *http.Server, timeout time.Duration) (string, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, timeout) }()
	return "http://" + ln.Addr().String(), done
}

func TestNewServerAplicaLosTimeoutsDelBorde(t *testing.T) {
	srv := newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, quiero un valor > 0 (slowloris)", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, quiero un valor > 0", srv.IdleTimeout)
	}
	// WriteTimeout debe quedar en 0 a propósito: la extracción puede tardar hasta
	// ORCH_TIMEOUT y un WriteTimeout corto cortaría requests legítimos en vuelo.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, quiero 0 (lo acota el timeout del downstream)", srv.WriteTimeout)
	}
}

func TestServeEsperaLosRequestsEnVueloAntesDeCerrar(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var unaVez sync.Once
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		unaVez.Do(func() { close(entered) })
		<-release
		return &domain.ProcessResult{DocumentID: "doc-1", Status: domain.StatusProcessed, Checksum: "abc"}, nil
	}}

	srv := newServer(handler.NewRouter(svc, testConfig(), logDiscard()))
	ctx, cancel := context.WithCancel(context.Background())
	baseURL, done := startServer(t, ctx, srv, 2*time.Second)

	resCh := make(chan httpResult, 1)
	go func() {
		res, err := http.DefaultClient.Do(processRequest(t, baseURL))
		resCh <- httpResult{res, err}
	}()

	<-entered
	cancel() // equivale a recibir SIGTERM

	select {
	case err := <-done:
		t.Fatalf("serve retornó (%v) antes de que terminara el request en vuelo", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	got := <-resCh
	if got.err != nil {
		t.Fatalf("el request en vuelo falló: %v", got.err)
	}
	defer got.res.Body.Close()
	if got.res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quiero 200: el cierre no debe cortar el request en vuelo", got.res.StatusCode)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve debe terminar limpio tras el cierre graceful, err=%v", err)
	}
}

func TestServeAplicaElTimeoutDeCierre(t *testing.T) {
	entered := make(chan struct{})
	bloqueado := make(chan struct{})
	var unaVez sync.Once
	svc := stubService{process: func(_ context.Context, _ *domain.ProcessInput) (*domain.ProcessResult, error) {
		unaVez.Do(func() { close(entered) })
		<-bloqueado // nunca responde: simula un downstream colgado
		return nil, nil
	}}

	srv := newServer(handler.NewRouter(svc, testConfig(), logDiscard()))
	ctx, cancel := context.WithCancel(context.Background())
	baseURL, done := startServer(t, ctx, srv, 100*time.Millisecond)

	// El resultado del request se ignora a propósito: tras el timeout el handler
	// sigue colgado y el proceso muere, que es el comportamiento documentado.
	go func() {
		if res, err := http.DefaultClient.Do(processRequest(t, baseURL)); err == nil {
			res.Body.Close()
		}
	}()

	<-entered
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("serve = %v, quiero un error por timeout de cierre", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve no terminó pese al timeout de cierre")
	}

	close(bloqueado) // liberamos el handler colgado para no dejar la goroutine viva
}
