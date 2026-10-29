# Postman — Orquestador PaperSoul

Colección para probar el flujo completo del Orquestador sin usar `curl`.

## Archivos

| Archivo | Qué es |
|---|---|
| `PaperSoul-Orquestador.postman_collection.json` | Colección con 3 requests: PDF real, PDF inválido (422) y multipart sin `file` (400). |
| `local.postman_environment.json` | Entorno para `go run ./cmd/orchestrator` o un contenedor con el puerto 8080 publicado. |
| `traefik.postman_environment.json` | Entorno para el stack de Compose detrás de Traefik (`https://127.0.0.1` + header `Host`). |

Usar **Postman Desktop**: la versión web no permite seleccionar archivos locales.

## Importar

1. Abrir Postman.
2. **Import** → arrastrar los 3 archivos (o la colección y el entorno que corresponda).
3. Elegir el entorno en el selector de arriba a la derecha.

## Entornos

La colección ya trae como default los valores de **Traefik**, así que funciona incluso sin
seleccionar un entorno.

| Entorno | `baseUrl` | `hostHeader` | Cuándo |
|---|---|---|---|
| Traefik | `https://127.0.0.1` | `orquestador.universidad.localhost` | Stack de Compose. El puerto 8080 no se publica: se llega por Traefik. |
| Local directo | `http://localhost:8080` | `localhost` | `go run ./cmd/orchestrator` o `docker run -p 8080:8080`. |

Con Traefik, desactivar la verificación del certificado wildcard local:

**Settings → General → SSL certificate verification → OFF**

Es un ajuste global de Postman, no del entorno. Solo para este entorno local.

Si preferís usar el nombre DNS en vez de `127.0.0.1`, editá el entorno y poné:

```text
baseUrl    = https://orquestador.universidad.localhost
hostHeader = orquestador.universidad.localhost
```

En Windows, `orquestador.universidad.localhost` puede no resolver. Si Postman no conecta con ese nombre, agregá esta línea a `C:\Windows\System32\drivers\etc\hosts` (requiere permisos de administrador):

```text
127.0.0.1 orquestador.universidad.localhost
```

La alternativa es dejar `baseUrl = https://127.0.0.1` y el header `Host` que ya trae el entorno de Traefik.

## Enviar un PDF

1. Abrir el request **Documentos → Procesar PDF**.
2. Ir a **Body → form-data**.
3. En la fila `file`, verificar que el tipo sea **File** (no Text).
4. **Select File** y elegir un PDF, por ejemplo de `../tests/stress/pdfs/`.
5. **Send**.

No agregar el header `Content-Type` a mano: Postman arma el boundary del multipart. El header `X-Correlation-Id` ya viene con `{{$guid}}`; si se envía, el servidor lo refleja en la respuesta y en los logs.

Respuesta esperada:

```json
{
  "documentId": "...",
  "status": "PROCESSED",
  "checksum": "...",
  "metadata": {
    "fileName": "Filosofia Lean.pdf",
    "sizeBytes": 123456,
    "pageCount": 42,
    "encrypted": false
  }
}
```

Si se envía el mismo PDF otra vez, `status` es `REUSED` y no se vuelve a extraer. Los tests de la request aceptan los dos valores.

## Casos de error

Los otros dos requests son determinísticos y no necesitan elegir un archivo:

| Request | Qué manda | Esperado |
|---|---|---|
| **PDF inválido (422)** | Campo `file` de texto: `esto no es un PDF` | `422` `invalid-pdf-header`, `invalid_params: file=invalid_magic_bytes`. |
| **Sin campo file (400)** | Campo `otro` de texto | `400` `invalid-multipart`, `invalid_params: file=required`. |

Ambos verifican `Content-Type: application/problem+json` y el `correlation id` en `instance`.

## Si no funciona

| Síntoma | Causa probable | Solución |
|---|---|---|
| `ECONNREFUSED 127.0.0.1:8080` | Está seleccionado el entorno *Local directo*, pero el stack de Compose no publica el 8080. | Seleccionar el entorno *Traefik* o quitar el entorno para usar el default. |
| `404 page not found` (texto plano) | Llegó a Traefik sin el header `Host`. | Verificar que el header `Host` esté habilitado y que el entorno *Traefik* esté seleccionado. Si Postman lo ignora, usar la opción del archivo `hosts` de abajo. |
| `SSL Error: Self-signed certificate` / `UNABLE_TO_VERIFY_LEAF_SIGNATURE` | Verificación SSL activada. | **Settings → General → SSL certificate verification → OFF**. |
| `502 persistence-unavailable` | MongoDB o Persistencia no están listos. | `./dev-up.sh` desde la raíz del proyecto y reintentar. |
| Falla *metadata.fileName no vacío* o *metadata.pageCount al menos 1*, pero `sizeBytes` es correcto | La imagen de Persistencia que está corriendo no devuelve `filename` ni `page_count` en el `201` (imagen vieja). | Reconstruir Persistencia: `./dev-up.sh --rebuild` o `docker build -t papersoul-persistencia:latest PaperSoul-Microservicio-Persistencia` y recrear el servicio. |
| Falla el test *PDF seleccionado en Body → form-data → file* | No se eligió el archivo. | **Body → form-data → file → Select File**. |
| No aparece la opción de elegir archivo | Se está usando Postman Web. | Usar **Postman Desktop**: la versión web no sube archivos locales. |

Para confirmar que el backend responde desde Windows, este `curl` tiene que dar `200`:

```powershell
curl.exe -k -sS -X POST "https://127.0.0.1/api/v1/documents/process" -H "Host: orquestador.universidad.localhost" -F "file=@tests/stress/pdfs/Filosofia Lean.pdf;type=application/pdf"
```

Si ese comando funciona y Postman no, el problema está en el request de Postman (entorno,
header `Host`, SSL o archivo), no en el Orquestador.

## Notas

- La ruta del PDF queda guardada como **ruta absoluta** dentro de la colección. Si se comparte, cada persona tiene que volver a seleccionar el archivo.
- La colección no incluye `GET /health`: por Traefik ese path no está expuesto a propósito. El liveness se prueba con `docker run --network mired curlimages/curl -sS http://orchestrator:8080/health`, como explica el README principal.
- No hay autenticación entre microservicios: quien pueda llegar al endpoint puede subir un PDF.
