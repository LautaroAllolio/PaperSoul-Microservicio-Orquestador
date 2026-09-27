GO  ?= go
PKG := ./...

.PHONY: build test race vet integration lint-openapi fmt check-fmt ci

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

fmt:
	$(GO) fmt $(PKG)

# gofmt como puerta, no como sugerencia: el repo formatea siempre con gofmt y un
# archivo sin formatear es un merge conflict garantizado.
check-fmt:
	@unformatted=$$(gofmt -l . | grep -v '^$$'); \
	if [ -n "$$unformatted" ]; then \
		echo "archivos sin gofmt (corrélos con 'make fmt'):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

# Lo mismo que corre el pipeline. El orden va de lo barato a lo caro: formateo y
# vet antes de pagar las dos corridas completas de tests.
ci: check-fmt vet test race integration lint-openapi
