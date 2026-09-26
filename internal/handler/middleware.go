package handler

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	errorsvc "github.com/papersoul/orchestrator/internal/platform/errors"
	"github.com/papersoul/orchestrator/internal/platform/problem"
	"github.com/papersoul/orchestrator/internal/platform/reqid"
)

// retryAfterSeconds es el Retry-After del 503 por saturación. El orquestador no
// se auto-recupera: el cliente debe reintentar, y un segundo alcanza para que el
// semáforo se vacíe en el caso normal de una ráfaga corta.
const retryAfterSeconds = "1"

// recorder memoriza el status, los bytes escritos y si la respuesta ya empezó.
// Envuelve el ResponseWriter solo para eso: la API responde JSON, así que no
// necesita Flush ni Hijack.
type recorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (rec *recorder) WriteHeader(code int) {
	if rec.wroteHeader {
		return
	}
	rec.status = code
	rec.wroteHeader = true
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK) // igual que net/http: escribir implica 200
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.written += int64(n)
	return n, err
}

// code devuelve el status final; una respuesta sin WriteHeader explícito es 200.
func (rec *recorder) code() int {
	if !rec.wroteHeader {
		return http.StatusOK
	}
	return rec.status
}

// RequestID resuelve el correlation id y lo propaga por el contexto.
//
// Acepta el X-Correlation-Id entrante solo si es un UUID válido (RFC 4122); si no
// lo es, genera uno. El valor se refleja en la respuesta y en los logs, así que
// aceptar cualquier cadena permitiría inyectar headers y ampliar logs a voluntad
// del cliente. El id generado también se escribe en la respuesta, que es lo que
// el cliente necesita para correlacionar su reintento.
func RequestID(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(reqid.Header)
			if !reqid.Valid(id) {
				if id != "" {
					// No se loguea el valor recibido: es entrada no confiable.
					logger.Warn("X-Correlation-Id inválido, se genera uno nuevo",
						"received_length", len(id), "path", r.URL.Path)
				}
				id = reqid.Generate()
			}
			w.Header().Set(reqid.Header, id)
			next.ServeHTTP(w, r.WithContext(reqid.With(r.Context(), id)))
		})
	}
}

// RequestLogger deja una línea por request con lo justo para operarlo.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			// La duración va con unidad explícita: el handler JSON de slog
			// serializa un time.Duration como nanos enteros, que hay que interpretar.
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.code(),
				"bytes", rec.written,
				"duration_ms", float64(time.Since(start).Microseconds())/1000,
				"correlation_id", reqid.From(r.Context()),
			)
		})
	}
}

// Recoverer convierte un panic en un 500 problem+json. El stack va al log del
// servidor; el cliente no ve ni el mensaje ni el stack.
//
// Va por dentro del logger a propósito: si el panic escapara hacia él, la línea
// de log saldría con status=200 y el incidente sería invisible en el registro.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w}
			defer func() {
				rv := recover()
				if rv == nil {
					return
				}
				logger.Error("panic en un handler",
					"panic", rv,
					"method", r.Method,
					"path", r.URL.Path,
					"correlation_id", reqid.From(r.Context()),
					"stack", string(debug.Stack()),
				)
				if rec.wroteHeader {
					// La respuesta ya empezó: no se puede reescribir, solo queda el log.
					return
				}
				problem.Write(rec, r, errorsvc.Map(r, errorsvc.ErrInternal))
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// MaxConcurrency limita los requests en vuelo. Al saturarse responde 503 con
// Retry-After en vez de encolar: encolado sin límite solo cambia la forma del
// problema (el cliente igual agota su propio timeout) y el body en memoria crece.
func MaxConcurrency(limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		sem := make(chan struct{}, limit)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", retryAfterSeconds)
				problem.Write(w, r, errorsvc.Map(r, errorsvc.ErrTooManyRequests))
			}
		})
	}
}
