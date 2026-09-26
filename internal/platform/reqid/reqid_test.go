package reqid_test

import (
	"context"
	"testing"

	"github.com/papersoul/orchestrator/internal/platform/reqid"
)

func TestFromSinValorDevuelveCadenaVacia(t *testing.T) {
	if got := reqid.From(context.Background()); got != "" {
		t.Fatalf("From(ctx sin valor) = %q, quiero %q", got, "")
	}
}

func TestWithGuardaElID(t *testing.T) {
	ctx := reqid.With(context.Background(), "8a2f9d1e-0000-0000-0000-000000000000")

	if got := reqid.From(ctx); got != "8a2f9d1e-0000-0000-0000-000000000000" {
		t.Fatalf("From = %q, quiero el ID guardado", got)
	}
}

func TestWithSobrescribeElIDPrevio(t *testing.T) {
	ctx := reqid.With(context.Background(), "primero")
	ctx = reqid.With(ctx, "segundo")

	if got := reqid.From(ctx); got != "segundo" {
		t.Fatalf("From = %q, quiero %q", got, "segundo")
	}
}

func TestWithIDVacioLoBorra(t *testing.T) {
	ctx := reqid.With(context.Background(), "8a2f9d1e")
	ctx = reqid.With(ctx, "")

	if got := reqid.From(ctx); got != "" {
		t.Fatalf("From = %q, quiero %q", got, "")
	}
}

func TestFromIgnoraValoresAjenosDelContexto(t *testing.T) {
	type claveAjena struct{}

	ctx := context.WithValue(context.Background(), claveAjena{}, "valor-de-otro-paquete")
	ctx = context.WithValue(ctx, "string", "otro-string") //nolint:staticcheck // clave no tipada: justamente no debe colisionar

	if got := reqid.From(ctx); got != "" {
		t.Fatalf("From = %q, quiero %q: la clave de reqid no debe colisionar", got, "")
	}
}

func TestNoMutaElContextoOriginal(t *testing.T) {
	base := context.Background()
	ctx := reqid.With(base, "8a2f9d1e")

	if got := reqid.From(base); got != "" {
		t.Fatalf("el contexto original quedó mutado: From(base) = %q", got)
	}
	if ctx == base { //nolint:staticcheck // comparamos contextos para detectar falta de derive
		t.Fatal("With devolvió el mismo contexto: debería derivar uno nuevo")
	}
}
