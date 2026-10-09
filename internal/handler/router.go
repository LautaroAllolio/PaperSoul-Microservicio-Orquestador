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

// ProcessPath es el endpoint de procesamiento de documentos.
const ProcessPath = "/api/v1/documents/process"

// HealthPath es el endpoint de liveness: confirma que el proceso atiende HTTP.
// No es readiness: no consulta Extracción ni Persistencia.
const HealthPath = "/health"

// rutas es la tabla de la API: path -> métodos admitidos. El ruteo lo declara
// NewRouter; esta tabla se usa para completar el header Allow del 405, que chi
// no expone (guarda la lista de métodos permitidos en un campo privado de su
// contexto de ruteo).
var rutas = map[string][]string{
	ProcessPath: {http.MethodPost},
	HealthPath:  {http.MethodGet},
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

	mux.Method(http.MethodPost, ProcessPath, process)
	mux.Method(http.MethodGet, HealthPath, http.HandlerFunc(health))

	return RequestID(logger)(
		RequestLogger(logger)(
			Recoverer(logger)(
				MaxConcurrency(cfg.MaxConcurrency, HealthPath)(mux))))
}

func allowFor(path string) string {
	return strings.Join(rutas[path], ", ")
}

// health responde 200 mientras el proceso atiende HTTP. No toca los downstream
// ni la configuración: es liveness del Orquestador, no readiness de sus
// dependencias. Queda fuera de MaxConcurrency para que una instancia saturada
// siga declarándose viva.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
