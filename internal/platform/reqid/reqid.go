// Package reqid propaga el X-Correlation-Id por el contexto de la request para
// que cada capa (handler, orquestador, clients HTTP) lo use sin firmarlo una y
// otra vez en cada struct.
package reqid

import "context"

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
