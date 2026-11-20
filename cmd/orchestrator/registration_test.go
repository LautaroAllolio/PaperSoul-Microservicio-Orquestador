package main

import (
	"net"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestBuildTagsIncluyeElContratoDeTraefikPorConsul(t *testing.T) {
	// testConfig replica los defaults del binario: la lista debe ser byte a
	// byte idéntica a la que generaba el código hardcodeado.
	tags := buildTags(testConfig(), 8080)

	want := []string{
		"traefik.enable=true",
		"traefik.http.routers.orchestrator.entrypoints=https",
		"traefik.http.routers.orchestrator.rule=Host(`orquestador.universidad.localhost`) && Path(`/api/v1/documents/process`)",
		"traefik.http.routers.orchestrator.tls=true",
		"traefik.http.routers.orchestrator.middlewares=orchestrator-retry",
		"traefik.http.services.orchestrator.loadbalancer.server.port=8080",
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.path=/health",
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.interval=10s",
		"traefik.http.services.orchestrator.loadbalancer.healthcheck.timeout=3s",
		"traefik.http.middlewares.orchestrator-retry.retry.attempts=2",
		"traefik.http.middlewares.orchestrator-retry.retry.initialinterval=500ms",
	}

	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags con defaults = %#v, quiero %#v", tags, want)
	}
}

func TestBuildTagsUsaLaConfiguracion(t *testing.T) {
	cfg := testConfig()
	cfg.PublicHost = "docs.otro.localhost"
	cfg.TraefikEntrypoint = "websecure"
	cfg.TraefikRouter = "docs-router"
	cfg.TraefikRetryAttempts = 5
	cfg.TraefikRetryInterval = 750 * time.Millisecond
	cfg.TraefikHealthInterval = 20 * time.Second
	cfg.TraefikHealthTimeout = 4 * time.Second

	tags := buildTags(cfg, 9000)
	want := []string{
		"traefik.enable=true",
		"traefik.http.routers.docs-router.entrypoints=websecure",
		"traefik.http.routers.docs-router.rule=Host(`docs.otro.localhost`) && Path(`/api/v1/documents/process`)",
		"traefik.http.routers.docs-router.tls=true",
		"traefik.http.routers.docs-router.middlewares=docs-router-retry",
		"traefik.http.services.docs-router.loadbalancer.server.port=9000",
		"traefik.http.services.docs-router.loadbalancer.healthcheck.path=/health",
		"traefik.http.services.docs-router.loadbalancer.healthcheck.interval=20s",
		"traefik.http.services.docs-router.loadbalancer.healthcheck.timeout=4s",
		"traefik.http.middlewares.docs-router-retry.retry.attempts=5",
		"traefik.http.middlewares.docs-router-retry.retry.initialinterval=750ms",
	}

	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags custom = %#v, quiero %#v", tags, want)
	}
}

func TestServiceIDEsUnicoPorReplica(t *testing.T) {
	if got := serviceID("abc123", 8080); got != "orchestrator-abc123-8080" {
		t.Fatalf("serviceID = %q", got)
	}
	if got := serviceID("", 8080); got != "orchestrator-local-8080" {
		t.Fatalf("serviceID sin hostname = %q", got)
	}
	if serviceID("a", 8080) == serviceID("b", 8080) {
		t.Fatalf("dos réplicas no pueden compartir ID")
	}
}

func TestBuildRegistrationUsaLosIntervalosDeConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	cfg := testConfig()
	cfg.AdvertiseAddr = "172.18.0.5"
	cfg.ConsulCheckInterval = 9 * time.Second
	cfg.ConsulCheckTimeout = 2 * time.Second
	cfg.ConsulDeregisterAfter = 5 * time.Minute

	svc, err := buildRegistration(cfg, ln)
	if err != nil {
		t.Fatalf("buildRegistration: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	hostname, _ := os.Hostname()
	if want := serviceID(hostname, port); svc.ID != want {
		t.Errorf("ID = %q, quiero %q", svc.ID, want)
	}
	if svc.Name != consulServiceName {
		t.Errorf("Name = %q, quiero %q", svc.Name, consulServiceName)
	}
	if svc.Address != "172.18.0.5" || svc.Port != port {
		t.Errorf("Address/Port = %q/%d, quiero 172.18.0.5/%d", svc.Address, svc.Port, port)
	}
	if want := "http://172.18.0.5:" + strconv.Itoa(port) + "/health"; svc.Check.HTTP != want {
		t.Errorf("Check.HTTP = %q, quiero %q", svc.Check.HTTP, want)
	}
	if svc.Check.Interval != 9*time.Second {
		t.Errorf("Check.Interval = %s, quiero 9s", svc.Check.Interval)
	}
	if svc.Check.Timeout != 2*time.Second {
		t.Errorf("Check.Timeout = %s, quiero 2s", svc.Check.Timeout)
	}
	if svc.Check.DeregisterCriticalServiceAfter != 5*time.Minute {
		t.Errorf("Check.DeregisterCriticalServiceAfter = %s, quiero 5m", svc.Check.DeregisterCriticalServiceAfter)
	}
	if !reflect.DeepEqual(svc.Tags, buildTags(cfg, port)) {
		t.Errorf("Tags = %#v, quiero %#v", svc.Tags, buildTags(cfg, port))
	}
}
