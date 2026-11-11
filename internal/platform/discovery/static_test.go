package discovery

import (
	"context"
	"testing"

	"github.com/papersoul/orchestrator/internal/client"
)

func TestStaticDevuelveLasURLsConfiguradas(t *testing.T) {
	r := NewStatic("http://extractor:9000", "http://persistencia:8000")

	got, err := r.Resolve(context.Background(), client.ServiceExtractor)
	if err != nil {
		t.Fatalf("Resolve extractor: %v", err)
	}
	if got != "http://extractor:9000" {
		t.Fatalf("Resolve extractor = %q", got)
	}

	got, err = r.Resolve(context.Background(), client.ServicePersistence)
	if err != nil {
		t.Fatalf("Resolve persistencia: %v", err)
	}
	if got != "http://persistencia:8000" {
		t.Fatalf("Resolve persistencia = %q", got)
	}
}

func TestStaticFallaConServicioDesconocidoOVacio(t *testing.T) {
	r := NewStatic("http://extractor:9000", "")

	if _, err := r.Resolve(context.Background(), "otro"); err == nil {
		t.Fatalf("servicio desconocido debe fallar")
	}
	if _, err := r.Resolve(context.Background(), client.ServicePersistence); err == nil {
		t.Fatalf("servicio sin URL debe fallar")
	}
}
