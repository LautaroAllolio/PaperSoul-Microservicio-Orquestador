package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// defaultConsulTimeout acota cada consulta a Consul. Es independiente del
	// timeout downstream: un Consul lento se reporta como servicio no
	// disponible (502), nunca como 504 de la llamada al microservicio.
	defaultConsulTimeout = time.Second
	// defaultCacheTTL es la ventana máxima en la que el catálogo puede quedar
	// desactualizado.
	defaultCacheTTL = time.Second
	// maxConsulErrorBody acota el body de diagnóstico de una respuesta no-2xx.
	maxConsulErrorBody = 1 << 10
)

// ConsulOptions son los parámetros del resolver basado en Consul.
type ConsulOptions struct {
	// Address es la base de la API HTTP de Consul. Acepta "consul:8500" o
	// "http://consul:8500".
	Address string
	// Timeout acota cada consulta a Consul. Default: 1 s.
	Timeout time.Duration
	// TTL es cuánto vale el catálogo cacheado por servicio. Default: 1 s.
	TTL time.Duration
}

// Consul resuelve servicios contra el catálogo de Consul, cachea el resultado
// por servicio y reparte las llamadas con round-robin entre las instancias
// passing.
type Consul struct {
	address string
	timeout time.Duration
	ttl     time.Duration
	http    *http.Client
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
	rr    map[string]uint64

	group singleflight.Group
}

type cacheEntry struct {
	urls    []string
	expires time.Time
}

// healthEntry es el subconjunto de /v1/health/service que usa el resolver.
type healthEntry struct {
	Node struct {
		Address string
	} `json:"Node"`
	Service struct {
		Address string
		Port    int
	} `json:"Service"`
}

// NewConsul arma el resolver contra la API HTTP de Consul.
func NewConsul(opts ConsulOptions) (*Consul, error) {
	address := normalizeConsulAddress(opts.Address)
	if address == "" {
		return nil, errors.New("discovery: Consul requiere una dirección")
	}
	if _, err := url.Parse(address); err != nil {
		return nil, fmt.Errorf("discovery: dirección de Consul inválida %q: %w", opts.Address, err)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultConsulTimeout
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}

	return &Consul{
		address: address,
		timeout: timeout,
		ttl:     ttl,
		// Sin Timeout en el cliente: el deadline lo pone el contexto de cada
		// consulta, que es el que distingue "Consul lento" del timeout downstream.
		http:  &http.Client{},
		now:   time.Now,
		cache: make(map[string]cacheEntry),
		rr:    make(map[string]uint64),
	}, nil
}

// Resolve devuelve la base URL de una instancia passing del servicio, rotando
// con round-robin. El catálogo se cachea por TTL; al vencer, una sola consulta
// concurrente viaja a Consul y el resto espera ese resultado (singleflight).
func (c *Consul) Resolve(ctx context.Context, service string) (string, error) {
	urls, err := c.instances(ctx, service)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("discovery: Consul no tiene instancias passing de %q", service)
	}
	return urls[c.next(service)%uint64(len(urls))], nil
}

func (c *Consul) instances(ctx context.Context, service string) ([]string, error) {
	if urls, ok := c.cached(service); ok {
		return urls, nil
	}

	result, err, _ := c.group.Do(service, func() (any, error) {
		// Otra goroutine pudo llenar la caché mientras esperábamos el turno.
		if urls, ok := c.cached(service); ok {
			return urls, nil
		}
		urls, err := c.fetch(ctx, service)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.cache[service] = cacheEntry{urls: urls, expires: c.now().Add(c.ttl)}
		c.mu.Unlock()
		return urls, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]string), nil
}

func (c *Consul) cached(service string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[service]
	if !ok || !c.now().Before(entry.expires) {
		return nil, false
	}
	return entry.urls, true
}

// next devuelve el índice de round-robin del servicio y lo incrementa.
func (c *Consul) next(service string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.rr[service]
	c.rr[service] = n + 1
	return n
}

func (c *Consul) fetch(ctx context.Context, service string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.address + "/v1/health/service/" + url.PathEscape(service) + "?passing"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: armando consulta a Consul: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: consultando %q en Consul: %w", service, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxConsulErrorBody))
		return nil, fmt.Errorf("discovery: Consul respondió %d para %q: %s",
			resp.StatusCode, service, strings.TrimSpace(string(detail)))
	}

	var entries []healthEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("discovery: respuesta inválida de Consul para %q: %w", service, err)
	}

	urls := make([]string, 0, len(entries))
	for _, entry := range entries {
		host := entry.Service.Address
		if host == "" {
			host = entry.Node.Address
		}
		if host == "" || entry.Service.Port <= 0 {
			continue
		}
		urls = append(urls, "http://"+net.JoinHostPort(host, strconv.Itoa(entry.Service.Port)))
	}
	return urls, nil
}

// normalizeConsulAddress acepta la forma cómoda "consul:8500" y la de URL
// completa. Sin esquema, asume http.
func normalizeConsulAddress(raw string) string {
	address := strings.TrimRight(strings.TrimSpace(raw), "/")
	if address == "" {
		return ""
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	return address
}
