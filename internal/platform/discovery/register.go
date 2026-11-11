package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// initialRegisterBackoff y maxRegisterBackoff acotan el reintento de
	// registro cuando Consul no está disponible al arranque.
	initialRegisterBackoff = time.Second
	maxRegisterBackoff     = 30 * time.Second
	// registerTimeout acota cada llamada al agente de Consul.
	registerTimeout = 2 * time.Second
	// maxRegisterErrorBody acota el body de diagnóstico de una respuesta no-2xx.
	maxRegisterErrorBody = 1 << 10
)

// Check es el health check que Consul ejecuta contra la instancia.
type Check struct {
	HTTP                           string
	Interval                       time.Duration
	Timeout                        time.Duration
	DeregisterCriticalServiceAfter time.Duration
}

// Service es una instancia concreta que se registra en el agente de Consul.
type Service struct {
	ID      string
	Name    string
	Address string
	Port    int
	Tags    []string
	Check   Check
}

// RegisterOptions son los parámetros del registrador.
type RegisterOptions struct {
	// Address es la base de la API HTTP de Consul. Acepta "consul:8500" o
	// "http://consul:8500".
	Address string
	// Timeout acota cada llamada a Consul. Default: 2 s.
	Timeout time.Duration
	// Logger registra los reintentos; nil desactiva los logs.
	Logger *slog.Logger
}

// Registrar registra y desregistra instancias en el agente de Consul.
type Registrar struct {
	address string
	timeout time.Duration
	http    *http.Client
	logger  *slog.Logger

	// sleep es inyectable para que los tests no esperen el backoff real.
	sleep func(context.Context, time.Duration) error
}

type registerPayload struct {
	ID      string       `json:"ID"`
	Name    string       `json:"Name"`
	Address string       `json:"Address"`
	Port    int          `json:"Port"`
	Tags    []string     `json:"Tags"`
	Check   checkPayload `json:"Check"`
}

type checkPayload struct {
	HTTP                           string `json:"HTTP"`
	Interval                       string `json:"Interval"`
	Timeout                        string `json:"Timeout"`
	DeregisterCriticalServiceAfter string `json:"DeregisterCriticalServiceAfter"`
}

// NewRegistrar arma el registrador contra la API HTTP de Consul.
func NewRegistrar(opts RegisterOptions) (*Registrar, error) {
	address := normalizeConsulAddress(opts.Address)
	if address == "" {
		return nil, errors.New("discovery: el registro requiere una dirección de Consul")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = registerTimeout
	}
	r := &Registrar{
		address: address,
		timeout: timeout,
		// Sin Timeout en el cliente: el deadline lo pone el contexto de cada llamada.
		http:   &http.Client{},
		logger: opts.Logger,
	}
	r.sleep = r.wait
	return r, nil
}

// Register da de alta la instancia en el agente de Consul.
func (r *Registrar) Register(ctx context.Context, svc Service) error {
	payload, err := json.Marshal(toRegisterPayload(svc))
	if err != nil {
		return fmt.Errorf("discovery: serializando el registro de %q: %w", svc.ID, err)
	}
	return r.put(ctx, "/v1/agent/service/register", payload)
}

// Deregister da de baja la instancia del agente de Consul.
func (r *Registrar) Deregister(ctx context.Context, serviceID string) error {
	return r.put(ctx, "/v1/agent/service/deregister/"+url.PathEscape(serviceID), nil)
}

// RegisterWithRetry intenta registrar de inmediato y, si Consul no responde,
// reintenta con backoff exponencial hasta lograrlo o hasta que ctx se cancele.
// No bloquea el arranque de la aplicación: se pensó para correr en background.
func (r *Registrar) RegisterWithRetry(ctx context.Context, svc Service) error {
	backoff := initialRegisterBackoff
	for {
		err := r.Register(ctx, svc)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.logger != nil {
			r.logger.Warn("registro en Consul falló; reintentando",
				"service_id", svc.ID, "backoff", backoff.String(), "error", err)
		}
		if err := r.sleep(ctx, backoff); err != nil {
			return err
		}
		backoff = nextBackoff(backoff)
	}
}

func (r *Registrar) put(ctx context.Context, path string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.address+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discovery: armando request a Consul: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("discovery: llamando a Consul: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxRegisterErrorBody))
		return fmt.Errorf("discovery: Consul respondió %d en %s: %s",
			resp.StatusCode, path, strings.TrimSpace(string(detail)))
	}
	return nil
}

func (r *Registrar) wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxRegisterBackoff {
		return maxRegisterBackoff
	}
	return next
}

func toRegisterPayload(svc Service) registerPayload {
	return registerPayload{
		ID:      svc.ID,
		Name:    svc.Name,
		Address: svc.Address,
		Port:    svc.Port,
		Tags:    svc.Tags,
		Check: checkPayload{
			HTTP:                           svc.Check.HTTP,
			Interval:                       svc.Check.Interval.String(),
			Timeout:                        svc.Check.Timeout.String(),
			DeregisterCriticalServiceAfter: svc.Check.DeregisterCriticalServiceAfter.String(),
		},
	}
}

// ResolveAdvertiseAddr devuelve la IP con la que la instancia se anuncia en
// Consul. Con varias redes, el override es la única forma de elegir la correcta.
func ResolveAdvertiseAddr(override string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		if ip := net.ParseIP(override); ip == nil {
			return "", fmt.Errorf("discovery: la dirección anunciada %q no es una IP válida", override)
		}
		return override, nil
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("discovery: listando interfaces de red: %w", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() {
			continue
		}
		return ip4.String(), nil
	}
	return "", errors.New("discovery: no hay una IPv4 no loopback; defina ORCH_ADVERTISE_ADDR")
}
