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
	"strconv"
	"syscall"
	"time"

	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	"github.com/papersoul/orchestrator/internal/platform/discovery"
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

	// Identidad con la que el Orquestador se registra en Consul. El host
	// público es el que cubre el certificado wildcard *.universidad.localhost.
	consulServiceName = "orchestrator"
	publicHost        = "orquestador.universidad.localhost"

	// Checks del laboratorio: 3 s detectan una réplica muerta bastante antes
	// que los 10 s clásicos; el timeout de 1 s evita colgar el check.
	consulCheckInterval   = 3 * time.Second
	consulCheckTimeout    = time.Second
	consulDeregisterAfter = time.Minute
	// deregisterTimeout acota el desregistro durante el cierre graceful.
	deregisterTimeout = 2 * time.Second
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

	resolver, err := newResolver(cfg)
	if err != nil {
		return fmt.Errorf("descubrimiento inválido: %w", err)
	}

	extractor := client.NewExtractor(client.Options{Resolver: resolver, Timeout: cfg.ExtractorTimeout})
	persistence := client.NewPersistence(client.Options{Resolver: resolver, Timeout: cfg.PersistenceTimeout})
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
		"discovery", cfg.Discovery,
		"consul", cfg.ConsulHTTPAddr,
		"extractor", cfg.ExtractorURL,
		"persistence", cfg.PersistenceURL,
		// En milisegundos y no como time.Duration: el handler JSON de slog
		// serializa una duración como nanos enteros, que salen como "1e+10".
		"shutdown_timeout_ms", milisegundos(cfg.ShutdownTimeout),
	)

	// En modo static no hay agente de Consul: la app arranca y sirve igual para
	// desarrollo local, tests e integración por etapas.
	if cfg.Discovery == "consul" {
		registrar, err := discovery.NewRegistrar(discovery.RegisterOptions{
			Address: cfg.ConsulHTTPAddr,
			Logger:  logger,
		})
		if err != nil {
			return fmt.Errorf("registro en Consul: %w", err)
		}

		svc, err := buildRegistration(cfg, ln)
		if err != nil {
			return fmt.Errorf("registro en Consul: %w", err)
		}

		regCtx, cancelReg := context.WithCancel(context.Background())
		regDone := make(chan struct{})
		go func() {
			defer close(regDone)
			if err := registrar.RegisterWithRetry(regCtx, svc); err != nil {
				if !errors.Is(err, context.Canceled) {
					logger.Warn("el registro en Consul se detuvo", "service_id", svc.ID, "error", err)
				}
				return
			}
			logger.Info("servicio registrado en Consul",
				"service_id", svc.ID, "address", svc.Address, "port", svc.Port)
		}()

		defer func() {
			// Primero se corta el loop de reintento y recién después se
			// desregistra: si no, un registro tardío reviviría la instancia.
			cancelReg()
			<-regDone

			dctx, cancel := context.WithTimeout(context.Background(), deregisterTimeout)
			defer cancel()
			if err := registrar.Deregister(dctx, svc.ID); err != nil {
				logger.Warn("no se pudo desregistrar de Consul", "service_id", svc.ID, "error", err)
				return
			}
			logger.Info("servicio desregistrado de Consul", "service_id", svc.ID)
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, srv, ln, cfg.ShutdownTimeout); err != nil {
		return err
	}
	logger.Info("cierre graceful completo")
	return nil
}

// buildRegistration arma la instancia que se publica en Consul: identidad
// única por réplica, IP anunciada, tags de Traefik y check de liveness.
func buildRegistration(cfg config.Config, ln net.Listener) (discovery.Service, error) {
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return discovery.Service{}, fmt.Errorf("el listener no expone un puerto TCP: %T", ln.Addr())
	}

	advertise, err := discovery.ResolveAdvertiseAddr(cfg.AdvertiseAddr)
	if err != nil {
		return discovery.Service{}, err
	}

	hostname, _ := os.Hostname() // si falla, serviceID cae a un nombre estable
	return discovery.Service{
		ID:      serviceID(hostname, tcpAddr.Port),
		Name:    consulServiceName,
		Address: advertise,
		Port:    tcpAddr.Port,
		Tags:    buildTags(tcpAddr.Port),
		Check: discovery.Check{
			HTTP: fmt.Sprintf("http://%s%s",
				net.JoinHostPort(advertise, strconv.Itoa(tcpAddr.Port)), handler.HealthPath),
			Interval:                       consulCheckInterval,
			Timeout:                        consulCheckTimeout,
			DeregisterCriticalServiceAfter: consulDeregisterAfter,
		},
	}, nil
}

// serviceID combina el nombre del servicio con el hostname del contenedor y el
// puerto: dos réplicas nunca comparten ID aunque corran en la misma red.
func serviceID(hostname string, port int) string {
	if hostname == "" {
		hostname = "local"
	}
	return fmt.Sprintf("%s-%s-%d", consulServiceName, hostname, port)
}

// buildTags traduce a tags de Consul el ruteo que antes vivía en labels Docker.
// Sin traefik.enable=true, Traefik ignora el servicio (exposedByDefault=false).
func buildTags(port int) []string {
	portTag := strconv.Itoa(port)
	return []string{
		"traefik.enable=true",
		"traefik.http.routers.orchestrator.entrypoints=https",
		"traefik.http.routers.orchestrator.rule=Host(`" + publicHost + "`) && Path(`" + handler.ProcessPath + "`)",
		"traefik.http.routers.orchestrator.tls=true",
		"traefik.http.routers.orchestrator.middlewares=orchestrator-retry",
		"traefik.http.services.orchestrator.loadbalancer.server.port=" + portTag,
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.path=" + handler.HealthPath,
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.interval=10s",
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.timeout=3s",
		"traefik.http.middlewares.orchestrator-retry.retry.attempts=2",
		"traefik.http.middlewares.orchestrator-retry.retry.initialinterval=500ms",
	}
}

// newResolver elige la implementación de descubrimiento según DISCOVERY. El
// modo se decide solo por configuración: nunca hay fallback en runtime.
func newResolver(cfg config.Config) (client.Resolver, error) {
	switch cfg.Discovery {
	case "static":
		return discovery.NewStatic(cfg.ExtractorURL, cfg.PersistenceURL), nil
	case "consul":
		return discovery.NewConsul(discovery.ConsulOptions{Address: cfg.ConsulHTTPAddr})
	default:
		return nil, fmt.Errorf("DISCOVERY desconocido %q", cfg.Discovery)
	}
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

	slog.Default().Info("señal recibida, cerrando el servidor", "timeout_ms", milisegundos(shutdownTimeout))

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

// milisegundos convierte una duración al número que hay que loguear. slog
// serializa time.Duration con su valor entero en nanosegundos, y en JSON eso
// sale como "1e+10": legible solo para quien sabe multiplicar por 1e-9.
func milisegundos(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
