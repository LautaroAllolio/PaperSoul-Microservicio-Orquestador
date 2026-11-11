package discovery

import (
	"context"
	"fmt"

	"github.com/papersoul/orchestrator/internal/client"
)

// Static resuelve los servicios contra un mapa fijo de URLs. Es el modo
// DISCOVERY=static: sirve para desarrollo local, tests e integración por
// etapas, sin Consul.
type Static struct {
	urls map[string]string
}

// NewStatic arma el resolver estático con las dos URLs downstream.
func NewStatic(extractorURL, persistenceURL string) *Static {
	return &Static{urls: map[string]string{
		client.ServiceExtractor:   extractorURL,
		client.ServicePersistence: persistenceURL,
	}}
}

// Resolve devuelve la URL configurada para el servicio.
func (s *Static) Resolve(_ context.Context, service string) (string, error) {
	url := s.urls[service]
	if url == "" {
		return "", fmt.Errorf("discovery: el servicio %q no tiene URL en modo static", service)
	}
	return url, nil
}
