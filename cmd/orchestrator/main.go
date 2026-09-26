// Comando orchestrator: microservicio que recibe un PDF, lo valida, lo extrae
// (Extracción) y persiste el resultado (Persistencia), reutilizando documentos
// ya vistos por checksum.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	"github.com/papersoul/orchestrator/internal/platform/pdf"
	"github.com/papersoul/orchestrator/internal/service"
)

const (
	// readHeaderTimeout acota el slowloris (headers que llegan lentos) sin
	// tocar el body: un WriteTimeout corto cortaría requests legítimos cuya
	// extracción dura hasta ORCH_TIMEOUT, que es justo lo que el cierre
	// graceful promete no hacer.
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

func main() {
	// JSON a stdout: los logs de un contenedor se recolectan como líneas.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("el orquestador terminó con error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuración inválida: %w", err)
	}

	extractor := client.NewExtractor(client.Options{BaseURL: cfg.ExtractorURL, Timeout: cfg.ExtractorTimeout})
	persistence := client.NewPersistence(client.Options{BaseURL: cfg.PersistenceURL, Timeout: cfg.PersistenceTimeout})
	orchestrator := service.NewOrchestrator(extractor, persistence, pdf.New(cfg.ValidationRelaxed))

	srv := newServer(handler.NewRouter(orchestrator, cfg, logger))

	// El listener se abre antes de servir para poder loguear el puerto real
	// (importa con ORCH_ADDR=127.0.0.1:0) y para no entrar en estado de
	// escucha si el bind falla.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("no se pudo escuchar en %q: %w", cfg.Addr, err)
	}

	logger.Info("orquestador escuchando",
		"addr", ln.Addr().String(),
		"max_concurrency", cfg.MaxConcurrency,
		"extractor", cfg.ExtractorURL,
		"persistence", cfg.PersistenceURL,
		"shutdown_timeout", cfg.ShutdownTimeout,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, srv, ln, cfg.ShutdownTimeout); err != nil {
		return err
	}
	logger.Info("cierre graceful completo")
	return nil
}

func newServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		// WriteTimeout queda en 0 a propósito; ver readHeaderTimeout.
	}
}

// serve atiende requests hasta que ctx se cancela (SIGINT/SIGTERM) y entonces
// cierra gracefulmente: deja de aceptar conexiones nuevas y espera a que terminen
// los requests en vuelo hasta shutdownTimeout. Si el timeout se agota, devuelve
// el error: el proceso muere y los requests colgados se cortan, que es el
// comportamiento documentado de ORCH_SHUTDOWN_TIMEOUT.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, shutdownTimeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return normalizeServeErr(err)
	case <-ctx.Done():
	}

	slog.Default().Info("señal recibida, cerrando el servidor", "timeout", shutdownTimeout)

	// Contexto nuevo: el que se canceló es el de la señal, no sirve para esperar.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("el cierre graceful agotó el timeout de %s: %w", shutdownTimeout, err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return normalizeServeErr(err)
	}
	return nil
}

func normalizeServeErr(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("el servidor dejó de servir: %w", err)
}
