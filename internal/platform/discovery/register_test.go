package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fakeAgent struct {
	requests   int32
	failFirst  int32
	lastBody   []byte
	lastPath   string
	lastMethod string
}

func (f *fakeAgent) handler(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt32(&f.requests, 1)
	body, _ := io.ReadAll(r.Body)
	f.lastBody = body
	f.lastPath = r.URL.Path
	f.lastMethod = r.Method
	if n <= atomic.LoadInt32(&f.failFirst) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func newFakeAgent(t *testing.T) (*fakeAgent, *httptest.Server) {
	t.Helper()
	f := &fakeAgent{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, srv
}

func newTestRegistrar(t *testing.T, addr string) *Registrar {
	t.Helper()
	r, err := NewRegistrar(RegisterOptions{Address: addr, Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewRegistrar: %v", err)
	}
	return r
}

func testService() Service {
	return Service{
		ID:      "orchestrator-test-8080",
		Name:    "orchestrator",
		Address: "10.0.0.5",
		Port:    8080,
		Tags:    []string{"traefik.enable=true", "traefik.http.services.orchestrator.loadbalancer.server.port=8080"},
		Check: Check{
			HTTP:                           "http://10.0.0.5:8080/health",
			Interval:                       3 * time.Second,
			Timeout:                        time.Second,
			DeregisterCriticalServiceAfter: time.Minute,
		},
	}
}

func TestRegistrarRegistraServicioConTagsYCheck(t *testing.T) {
	f, srv := newFakeAgent(t)
	r := newTestRegistrar(t, srv.URL)

	if err := r.Register(context.Background(), testService()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if f.lastMethod != http.MethodPut {
		t.Fatalf("method = %q, quiero PUT", f.lastMethod)
	}
	if f.lastPath != "/v1/agent/service/register" {
		t.Fatalf("path = %q", f.lastPath)
	}

	var payload map[string]any
	if err := json.Unmarshal(f.lastBody, &payload); err != nil {
		t.Fatalf("payload no es JSON: %v", err)
	}
	if payload["ID"] != "orchestrator-test-8080" {
		t.Fatalf("ID = %v", payload["ID"])
	}
	if payload["Name"] != "orchestrator" {
		t.Fatalf("Name = %v", payload["Name"])
	}
	if payload["Address"] != "10.0.0.5" {
		t.Fatalf("Address = %v", payload["Address"])
	}
	if payload["Port"] != float64(8080) {
		t.Fatalf("Port = %v", payload["Port"])
	}
	tags, ok := payload["Tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "traefik.enable=true" {
		t.Fatalf("Tags = %v", payload["Tags"])
	}

	check, ok := payload["Check"].(map[string]any)
	if !ok {
		t.Fatalf("falta Check en el payload: %v", payload)
	}
	wantCheck := map[string]string{
		"HTTP":                           "http://10.0.0.5:8080/health",
		"Interval":                       "3s",
		"Timeout":                        "1s",
		"DeregisterCriticalServiceAfter": "1m0s",
	}
	for key, want := range wantCheck {
		if check[key] != want {
			t.Fatalf("Check[%q] = %v, quiero %q", key, check[key], want)
		}
	}
}

func TestRegistrarReintentaConBackoffHastaLograrElRegistro(t *testing.T) {
	f, srv := newFakeAgent(t)
	atomic.StoreInt32(&f.failFirst, 2)
	r := newTestRegistrar(t, srv.URL)
	r.sleep = func(context.Context, time.Duration) error { return nil }

	if err := r.RegisterWithRetry(context.Background(), testService()); err != nil {
		t.Fatalf("RegisterWithRetry: %v", err)
	}
	if got := atomic.LoadInt32(&f.requests); got != 3 {
		t.Fatalf("intentos = %d, quiero 3 (2 fallos + 1 éxito)", got)
	}
}

func TestRegistrarSeDetieneAlCancelarElContexto(t *testing.T) {
	f, srv := newFakeAgent(t)
	atomic.StoreInt32(&f.failFirst, 1_000) // siempre falla: fuerza el backoff
	r := newTestRegistrar(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	r.sleep = func(context.Context, time.Duration) error {
		cancel()
		return nil
	}

	err := r.RegisterWithRetry(ctx, testService())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, quiero context.Canceled", err)
	}
}

func TestRegistrarDevuelveErrorSiConsulRespondeNo200(t *testing.T) {
	f, srv := newFakeAgent(t)
	atomic.StoreInt32(&f.failFirst, 1)
	r := newTestRegistrar(t, srv.URL)

	if err := r.Register(context.Background(), testService()); err == nil {
		t.Fatalf("respuesta 500 de Consul debe devolver error")
	}
}

func TestRegistrarDeregistraElServicio(t *testing.T) {
	f, srv := newFakeAgent(t)
	r := newTestRegistrar(t, srv.URL)

	if err := r.Deregister(context.Background(), "orchestrator-test-8080"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if f.lastMethod != http.MethodPut {
		t.Fatalf("method = %q, quiero PUT", f.lastMethod)
	}
	if f.lastPath != "/v1/agent/service/deregister/orchestrator-test-8080" {
		t.Fatalf("path = %q", f.lastPath)
	}
}

func TestNewRegistrarRechazaDireccionVacia(t *testing.T) {
	if _, err := NewRegistrar(RegisterOptions{}); err == nil {
		t.Fatalf("NewRegistrar sin Address debe fallar")
	}
}

func TestResolveAdvertiseAddrRespetaElOverride(t *testing.T) {
	got, err := ResolveAdvertiseAddr("172.18.0.7")
	if err != nil {
		t.Fatalf("ResolveAdvertiseAddr: %v", err)
	}
	if got != "172.18.0.7" {
		t.Fatalf("addr = %q, quiero 172.18.0.7", got)
	}
}

func TestResolveAdvertiseAddrAutodetectaUnaIPv4NoLoopback(t *testing.T) {
	got, err := ResolveAdvertiseAddr("")
	if err != nil {
		t.Skipf("el entorno no tiene una IPv4 no loopback: %v", err)
	}
	ip := net.ParseIP(got)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("addr = %q, quiero una IPv4", got)
	}
	if ip.IsLoopback() {
		t.Fatalf("addr = %q, no quiero loopback", got)
	}
}

func TestNextBackoffCreceYSeCapa(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{initialRegisterBackoff, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, maxRegisterBackoff},
		{maxRegisterBackoff, maxRegisterBackoff},
	}
	for _, tc := range cases {
		if got := nextBackoff(tc.in); got != tc.want {
			t.Fatalf("nextBackoff(%s) = %s, quiero %s", tc.in, got, tc.want)
		}
	}
}
