GO  ?= go
PKG := ./...

.PHONY: build test race vet integration lint-openapi

build:
	$(GO) build $(PKG)

test:
	$(GO) test $(PKG)

race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

integration:
	$(GO) test -tags=integration $(PKG)

# El contrato no se valida con una CLI externa: los invariantes que importan
# (matriz de status 2.6, problem+json en todo error, ejemplos coherentes) están
# en api/openapi_test.go, que corre con el resto de los tests. Este target sirve
# para iterar sobre el contrato sin pagar la suite completa.
lint-openapi:
	$(GO) test ./api/...
