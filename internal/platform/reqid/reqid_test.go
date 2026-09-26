package reqid_test

import (
	"context"
	"strings"
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

func TestValidAceptaUUIDsRFC4122(t *testing.T) {
	cases := map[string]string{
		"v4":         "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"v1":         "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"mayusculas": "3F2504E0-4F89-41D3-9A0C-0305E82C3301",
		"v7":         "018f6e4c-7a1b-7c2d-8e3f-4a5b6c7d8e9f",
		"ceros":      "00000000-0000-4000-8000-000000000000",
	}

	for nombre, id := range cases {
		t.Run(nombre, func(t *testing.T) {
			if !reqid.Valid(id) {
				t.Errorf("Valid(%q) = false, quiero true", id)
			}
		})
	}
}

func TestValidRechazaLoQueNoEsUUID(t *testing.T) {
	cases := map[string]string{
		"vacio":               "",
		"texto":               "no soy un uuid",
		"sin guiones":         "3f2504e04f8941d39a0c0305e82c3301",
		"corto":               "3f2504e0-4f89-41d3-9a0c-0305e82c33",
		"largo":               "3f2504e0-4f89-41d3-9a0c-0305e82c33012",
		"caracter no hex":     "3f2504e0-4f89-41d3-9a0c-0305e82c33zz",
		"variante invalida":   "3f2504e0-4f89-41d3-ca0c-0305e82c3301",
		"version invalida":    "3f2504e0-4f89-91d3-9a0c-0305e82c3301",
		"version cero":        "3f2504e0-4f89-01d3-9a0c-0305e82c3301",
		"espacio al final":    "3f2504e0-4f89-41d3-9a0c-0305e82c3301 ",
		"espacio al inicio":   " 3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"guion bajo":          "3f2504e0_4f89_41d3_9a0c_0305e82c3301",
		"solo guiones":        "------------------------------------",
		"inyeccion de header": "abc\r\nX-Injected: 1",
		"muy largo para logs": strings.Repeat("a", 512),
		"uuid con prefijo":    "urn:uuid:3f2504e0-4f89-41d3-9a0c-0305e82c3301",
	}

	for nombre, id := range cases {
		t.Run(nombre, func(t *testing.T) {
			if reqid.Valid(id) {
				t.Errorf("Valid(%q) = true, quiero false", id)
			}
		})
	}
}

func TestGenerateDevuelveUUIDsV4ValidosYUnicos(t *testing.T) {
	const n = 1000
	vistos := make(map[string]bool, n)

	for i := 0; i < n; i++ {
		id := reqid.Generate()
		if !reqid.Valid(id) {
			t.Fatalf("Generate() = %q, no es un UUID válido", id)
		}
		if len(id) != 36 {
			t.Fatalf("Generate() = %q, quiero 36 caracteres", id)
		}
		// Versión 4: la primera nibble del tercer grupo debe ser '4'.
		if id[14] != '4' {
			t.Fatalf("Generate() = %q, la nibble de versión debe ser 4", id)
		}
		if vistos[id] {
			t.Fatalf("Generate() repitió el valor %q en %d generaciones", id, n)
		}
		vistos[id] = true
	}
}
