package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/papersoul/orchestrator/internal/platform/config"
	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/problem"
)

// ProcessPath es el único endpoint del orquestador.
const ProcessPath = "/api/v1/documents/process"

// rutas es la tabla de la API: path -> métodos admitidos. Se usa para montar el
// mux y para completar el header Allow del 405, que chi no expone (guarda la
// lista de métodos permitidos en un campo privado de su contexto de ruteo).
var rutas = map[string][]string{
	ProcessPath: {http.MethodPost},
}

// NewRouter arma el borde HTTP completo: ruteo, middlewares y los handlers de
// error del router.
//
// Orden de los middlewares, de afuera hacia adentro:
//
//	RequestID      todo lo que sigue necesita el correlation id
//	RequestLogger  por fuera del recoverer, para registrar el 500 que genera
//	Recoverer      convierte cualquier panic en problem+json
//	MaxConcurrency lo más adentro posible: no se ocupa un slot con requests que
//	               ni van a ejecutar el handler
func NewRouter(svc DocumentService, cfg config.Config, logger *slog.Logger) http.Handler {
	process := NewProcessHandler(svc, cfg)

	mux := chi.NewRouter()

	// Sin esto chi responde 404 y 405 en text/plain y el cliente recibiría una
	// respuesta que no cumple RFC 9457.
	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, errorsvc.Map(r, errorsvc.ErrRouteNotFound))
	})
	mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		if allow := allowFor(r.URL.Path); allow != "" {
			w.Header().Set("Allow", allow)
		}
		problem.Write(w, r, errorsvc.Map(r, errorsvc.ErrMethodNotAllowed))
	})

	for path, methods := range rutas {
		for _, method := range methods {
			mux.Method(method, path, process)
		}
	}

	return RequestID(logger)(
		RequestLogger(logger)(
			Recoverer(logger)(
				MaxConcurrency(cfg.MaxConcurrency)(mux))))
}

func allowFor(path string) string {
	return strings.Join(rutas[path], ", ")
}
