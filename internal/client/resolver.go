package client

import "context"

// Nombres con los que cada microservicio se registra en Consul. Son también la
// clave con la que el resolver estático busca su URL.
const (
	ServiceExtractor   = "extractor"
	ServicePersistence = "persistencia"
)

// Resolver entrega la base URL de una instancia sana de un servicio.
//
// Es el puerto que consumen los clients: la implementación puede consultar
// Consul o devolver una URL fija, y los adapters HTTP no necesitan saber cuál.
type Resolver interface {
	Resolve(ctx context.Context, service string) (string, error)
}
