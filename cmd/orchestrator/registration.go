package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/papersoul/orchestrator/internal/handler"
	"github.com/papersoul/orchestrator/internal/platform/config"
	"github.com/papersoul/orchestrator/internal/platform/discovery"
)

// consulServiceName es la identidad lógica del servicio en Consul. Es distinta
// del router de Traefik: dos réplicas comparten este nombre pero no su ID.
const consulServiceName = "orchestrator"

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
		Tags:    buildTags(cfg, tcpAddr.Port),
		Check: discovery.Check{
			HTTP: fmt.Sprintf("http://%s%s",
				net.JoinHostPort(advertise, strconv.Itoa(tcpAddr.Port)), handler.HealthPath),
			Interval:                       cfg.ConsulCheckInterval,
			Timeout:                        cfg.ConsulCheckTimeout,
			DeregisterCriticalServiceAfter: cfg.ConsulDeregisterAfter,
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
//
// El orden es parte del contrato con el stack: el test de registro compara la
// lista completa con los defaults del binario.
func buildTags(cfg config.Config, port int) []string {
	portTag := strconv.Itoa(port)
	router := cfg.TraefikRouter
	retryMiddleware := router + "-retry"
	return []string{
		"traefik.enable=true",
		"traefik.http.routers." + router + ".entrypoints=" + cfg.TraefikEntrypoint,
		"traefik.http.routers." + router + ".rule=Host(`" + cfg.PublicHost + "`) && Path(`" + handler.ProcessPath + "`)",
		"traefik.http.routers." + router + ".tls=true",
		"traefik.http.routers." + router + ".middlewares=" + retryMiddleware,
		"traefik.http.services." + router + ".loadbalancer.server.port=" + portTag,
		"traefik.http.services." + router + ".loadbalancer.healthcheck.path=" + handler.HealthPath,
		"traefik.http.services." + router + ".loadbalancer.healthcheck.interval=" + cfg.TraefikHealthInterval.String(),
		"traefik.http.services." + router + ".loadbalancer.healthcheck.timeout=" + cfg.TraefikHealthTimeout.String(),
		"traefik.http.middlewares." + retryMiddleware + ".retry.attempts=" + strconv.Itoa(cfg.TraefikRetryAttempts),
		"traefik.http.middlewares." + retryMiddleware + ".retry.initialinterval=" + cfg.TraefikRetryInterval.String(),
	}
}
