package client_test

import (
	"github.com/papersoul/orchestrator/internal/client"
	"github.com/papersoul/orchestrator/internal/service"
)

// Las interfaces se declaran en el consumidor (service). Estas aserciones
// congelan que los adapters de esta capa las satisfacen, sin invertir la
// dirección de dependencias (service nunca importa client).
var (
	_ service.ExtractorClient   = (*client.Extractor)(nil)
	_ service.PersistenceClient = (*client.Persistence)(nil)
)
