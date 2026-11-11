package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
)

// Los tests viven en el paquete (no en discovery_test) a propósito: necesitan
// mover el reloj de la caché sin dormir un segundo real ni exponer un hook de
// test en la API pública.

type fakeInstance struct {
	nodeAddress    string
	serviceAddress string
	port           int
}

type fakeConsul struct {
	mu       sync.Mutex
	requests int32
	status   int
	delay    time.Duration
	release  chan struct{}
	services map[string][]fakeInstance
}

func newFakeConsul(t *testing.T, services map[string][]fakeInstance) (*fakeConsul, *httptest.Server) {
	t.Helper()
	f := &fakeConsul{services: services}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeConsul) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.requests, 1)

	f.mu.Lock()
	status, delay, release := f.status, f.delay, f.release
	service := strings.TrimPrefix(r.URL.Path, "/v1/health/service/")
	instances := f.services[service]
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if release != nil {
		<-release
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}

	type node struct {
		Address string
	}
	type svc struct {
		Address string
		Port    int
	}
	type entry struct {
		Node    node
		Service svc
	}
	entries := make([]entry, 0, len(instances))
	for _, in := range instances {
		entries = append(entries, entry{
			Node:    node{Address: in.nodeAddress},
			Service: svc{Address: in.serviceAddress, Port: in.port},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func (f *fakeConsul) requestCount() int {
	return int(atomic.LoadInt32(&f.requests))
}

func (f *fakeConsul) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeConsul) setDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = delay
}

func (f *fakeConsul) blockUntilReleased() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.release = make(chan struct{})
	return f.release
}

// fixedClock devuelve un reloj mutable para los tests de TTL.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFixedClock() *fixedClock {
	return &fixedClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestConsul(t *testing.T, addr string, clock *fixedClock) *Consul {
	t.Helper()
	c, err := NewConsul(ConsulOptions{Address: addr, Timeout: time.Second, TTL: time.Second})
	if err != nil {
		t.Fatalf("NewConsul: %v", err)
	}
	c.now = clock.Now
	return c
}

func TestConsulResuelveInstanciasPassingConRoundRobin(t *testing.T) {
	f, srv := newFakeConsul(t, map[string][]fakeInstance{
		client.ServiceExtractor: {
			{nodeAddress: "10.0.0.2", serviceAddress: "10.0.0.2", port: 9000},
			{nodeAddress: "10.0.0.3", serviceAddress: "10.0.0.3", port: 9000},
			{nodeAddress: "10.0.0.4", serviceAddress: "10.0.0.4", port: 9000},
		},
	})
	c := newTestConsul(t, srv.URL, newFixedClock())

	want := []string{
		"http://10.0.0.2:9000",
		"http://10.0.0.3:9000",
		"http://10.0.0.4:9000",
		"http://10.0.0.2:9000",
		"http://10.0.0.3:9000",
		"http://10.0.0.4:9000",
	}
	for i, w := range want {
		got, err := c.Resolve(context.Background(), client.ServiceExtractor)
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i, err)
		}
		if got != w {
			t.Fatalf("Resolve #%d = %q, quiero %q", i, got, w)
		}
	}
	if f.requestCount() != 1 {
		t.Fatalf("consultas a Consul = %d, quiero 1 (catálogo cacheado)", f.requestCount())
	}
}

func TestConsulUsaNodeAddressSiServiceAddressEstaVacio(t *testing.T) {
	_, srv := newFakeConsul(t, map[string][]fakeInstance{
		client.ServicePersistence: {{nodeAddress: "10.0.0.9", serviceAddress: "", port: 8000}},
	})
	c := newTestConsul(t, srv.URL, newFixedClock())

	got, err := c.Resolve(context.Background(), client.ServicePersistence)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "http://10.0.0.9:8000" {
		t.Fatalf("Resolve = %q, quiero %q", got, "http://10.0.0.9:8000")
	}
}

func TestConsulCacheaElCatalogoHastaQueVenceElTTL(t *testing.T) {
	f, srv := newFakeConsul(t, map[string][]fakeInstance{
		client.ServiceExtractor: {{nodeAddress: "10.0.0.2", serviceAddress: "10.0.0.2", port: 9000}},
	})
	clock := newFixedClock()
	c := newTestConsul(t, srv.URL, clock)

	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err != nil {
		t.Fatalf("primer Resolve: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err != nil {
		t.Fatalf("Resolve con caché vigente: %v", err)
	}
	if f.requestCount() != 1 {
		t.Fatalf("consultas antes de vencer el TTL = %d, quiero 1", f.requestCount())
	}

	clock.Advance(600 * time.Millisecond) // total 1.1 s: venció
	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err != nil {
		t.Fatalf("Resolve con caché vencida: %v", err)
	}
	if f.requestCount() != 2 {
		t.Fatalf("consultas tras vencer el TTL = %d, quiero 2", f.requestCount())
	}
}

func TestConsulColapsaConsultasConcurrentesCuandoVenceElTTL(t *testing.T) {
	f, srv := newFakeConsul(t, map[string][]fakeInstance{
		client.ServiceExtractor: {
			{nodeAddress: "10.0.0.2", serviceAddress: "10.0.0.2", port: 9000},
			{nodeAddress: "10.0.0.3", serviceAddress: "10.0.0.3", port: 9000},
		},
	})
	clock := newFixedClock()
	c := newTestConsul(t, srv.URL, clock)

	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err != nil {
		t.Fatalf("Resolve inicial: %v", err)
	}
	clock.Advance(2 * time.Second)

	release := f.blockUntilReleased()
	const goroutines = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err != nil {
				errs <- err
			}
		}()
	}

	// Damos tiempo a que todas las goroutines lleguen al singleflight mientras
	// la única consulta está bloqueada en el handler.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Resolve concurrente: %v", err)
	}

	if f.requestCount() != 2 {
		t.Fatalf("consultas a Consul = %d, quiero 2 (1 inicial + 1 refresco)", f.requestCount())
	}
}

func TestConsulCatalogoVacioDevuelveError(t *testing.T) {
	_, srv := newFakeConsul(t, map[string][]fakeInstance{})
	c := newTestConsul(t, srv.URL, newFixedClock())

	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err == nil {
		t.Fatalf("catálogo vacío debe devolver error")
	}
}

func TestConsulCaidoDevuelveError(t *testing.T) {
	_, srv := newFakeConsul(t, map[string][]fakeInstance{})
	c := newTestConsul(t, srv.URL, newFixedClock())
	srv.Close()

	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err == nil {
		t.Fatalf("Consul caído debe devolver error")
	}
}

func TestConsulTimeoutDevuelveError(t *testing.T) {
	f, srv := newFakeConsul(t, map[string][]fakeInstance{
		client.ServiceExtractor: {{nodeAddress: "10.0.0.2", serviceAddress: "10.0.0.2", port: 9000}},
	})
	f.setDelay(200 * time.Millisecond)

	c, err := NewConsul(ConsulOptions{Address: srv.URL, Timeout: 20 * time.Millisecond, TTL: time.Second})
	if err != nil {
		t.Fatalf("NewConsul: %v", err)
	}

	start := time.Now()
	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err == nil {
		t.Fatalf("timeout de Consul debe devolver error")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("Resolve tardó %s: no respetó el timeout propio", elapsed)
	}
}

func TestConsulRespuestaNo200DevuelveError(t *testing.T) {
	f, srv := newFakeConsul(t, map[string][]fakeInstance{})
	f.setStatus(http.StatusInternalServerError)
	c := newTestConsul(t, srv.URL, newFixedClock())

	if _, err := c.Resolve(context.Background(), client.ServiceExtractor); err == nil {
		t.Fatalf("respuesta 500 de Consul debe devolver error")
	}
}

func TestNewConsulRechazaDireccionVacia(t *testing.T) {
	if _, err := NewConsul(ConsulOptions{}); err == nil {
		t.Fatalf("NewConsul sin Address debe fallar")
	}
}

func TestNewConsulAceptaDireccionSinEsquema(t *testing.T) {
	c, err := NewConsul(ConsulOptions{Address: "consul:8500"})
	if err != nil {
		t.Fatalf("NewConsul: %v", err)
	}
	if c.address != "http://consul:8500" {
		t.Fatalf("address = %q, quiero %q", c.address, "http://consul:8500")
	}
}
