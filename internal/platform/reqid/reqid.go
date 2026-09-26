// Package reqid propaga el X-Correlation-Id por el contexto de la request para
// que cada capa (handler, orquestador, clients HTTP) lo use sin firmarlo una y
// otra vez en cada struct.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Header es el nombre del header que transporta el correlation id.
const Header = "X-Correlation-Id"

// ctxKey es privately typed: ninguna otra clave de contexto puede colisionar.
type ctxKey struct{}

// With devuelve un contexto derivado que transporta el correlation id.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From devuelve el correlation id del contexto, o "" si no hay ninguno.
// Un id vacío no se propaga: el client omite el header.
func From(ctx context.Context) string {
	id, ok := ctx.Value(ctxKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

// Generate devuelve un UUID v4 aleatorio. crypto/rand.Read nunca devuelve error
// (desde Go 1.24 entra en pánico ante un fallo catastrófico del sistema), así que
// no hay error que propagar.
func Generate() string {
	var b [16]byte
	_, _ = rand.Read(b[:])      //nolint:errcheck // crypto/rand: documentado como infallible
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante 10xx de RFC 4122

	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Valid dice si id es un UUID con la forma de RFC 4122: 8-4-4-4-12 dígitos
// hexadecimales, versión 1 a 8 y variante 10xx. Rechaza cualquier otra cosa
// (espacios, CR/LF, prefijos, cadenas largas) porque el valor se refleja en la
// respuesta y en los logs: aceptarlo sin filtro abriría la puerta a inyectar
// headers y a ampliar líneas de log a voluntad del cliente.
func Valid(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return false
			}
			continue
		}
		if !isHex(id[i]) {
			return false
		}
	}
	if v := hexValue(id[14]); v < 1 || v > 8 {
		return false
	}
	switch id[19] {
	case '8', '9', 'a', 'A', 'b', 'B':
	default:
		return false
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
