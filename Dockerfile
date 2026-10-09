# syntax=docker/dockerfile:1

# --- Etapa builder: compila el binario estático -------------------------------
# Trabaja como root para descargar módulos y compilar; esta imagen se descarta y
# no llega al contenedor final (ahí se corre como usuario no-root).
FROM golang:1.25-bookworm AS builder

WORKDIR /src

# Capa de dependencias: se cachea mientras go.mod / go.sum no cambien.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: binario estático puro Go (pdfcpu no usa CGO), apto para una base
# distroless sin libc. -trimpath y -s -w quitan rutas de build y símbolos.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/orchestrator ./cmd/orchestrator

# --- Etapa runtime: sólo el binario, como usuario no-root ---------------------
# distroless static ya trae las CA certs y define el usuario no-root (uid 65532);
# no incluye shell ni gestores de paquetes, lo que reduce la superficie de ataque.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/orchestrator /orchestrator

# Único valor operativo por defecto. EXTRACTOR_URL y PERSISTENCE_URL son
# obligatorias y se suministran en runtime: nunca quedan embebidas en la imagen.
ENV ORCH_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/orchestrator"]
