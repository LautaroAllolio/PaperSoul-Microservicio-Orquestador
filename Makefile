GO  ?= go
PKG := ./...

.PHONY: build test race vet integration

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