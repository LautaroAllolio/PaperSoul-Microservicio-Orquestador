# Tareas: Despliegue y operación del Orquestador

Este checklist acompaña a [`plan-deployment.md`](./plan-deployment.md). No reemplaza ni
modifica el plan previo de implementación de la API. Limitar todos los cambios de código y
documentación al repositorio del Orquestador.

## Fase 1: Contrato de despliegue

### Tarea 1: Acordar protocolos, descubrimiento y criterios operativos

**Descripción:** Resolver las preguntas abiertas del plan sobre HTTP/TCP frente a UDP,
proveedor/red de Traefik, health checks, retry/circuit breaker y pruebas de estrés.

**Criterios de aceptación:**
- [ ] Confirmado que el tráfico de la API actual es HTTP/TCP, o definido de forma concreta
      el requisito UDP y su alcance dentro del Orquestador.
- [ ] Registrados el proveedor de descubrimiento de Traefik, nombre de la red compartida,
      host/path de la API y el puerto interno del servicio.
- [x] Acordado `GET /health` como liveness del proceso, sin comprobar disponibilidad de
      Extractor ni Persistencia y sin readiness por ahora.
- [ ] Definidos errores reintentables, límite/espera de reintentos y comportamiento deseado
      al perder una réplica.
- [x] Localizados los archivos agregados: `tests/spike_tests.js` (k6) y
      `tests/test_carga.txt` (targets tipo Vegeta).
- [x] Adaptados los requests para probar `POST /api/v1/documents/process` con multipart
      `file`, el contrato de ingreso del Orquestador.
- [x] Recuperados los cuatro PDFs referenciados por k6; están en `tests/stress/pdfs/`.
- [x] K6 lee los PDFs desde sus ubicaciones existentes bajo `tests/stress/pdfs/`.
- [x] Vegeta usa payloads multipart generados localmente desde esos PDFs antes de la carga.
- [ ] Validar `go test ./tests` y realizar una corrida smoke de k6 y Vegeta contra una
      instancia accesible del Orquestador.
- [ ] Definidos los umbrales de aceptación y comandos/versión de las herramientas.

**Verificación:** Revisión del usuario de las decisiones escritas antes de configurar
routers, health checks o pruebas.

**Dependencias:** Ninguna.

**Archivos probables:** `tasks/plan-deployment.md`.

**Alcance estimado:** Pequeño.

### Tarea 2: Crear la imagen Docker del Orquestador

**Descripción:** Añadir una construcción reproducible de la aplicación Go en una imagen
adecuada para runtime, con configuración externalizada, usuario no privilegiado cuando sea
compatible y manejo correcto de señales/cierre.

**Criterios de aceptación:**
- [ ] La imagen se construye desde los manifiestos Go del Orquestador sin modificar otros
      microservicios.
- [ ] La imagen ejecuta el binario compilado, no descarga dependencias al iniciar y no
      requiere escribir documentos ni estado duradero en el filesystem.
- [ ] La configuración sensible/operativa se suministra en runtime, nunca queda embebida en
      la imagen.
- [ ] SIGTERM permite el cierre graceful existente dentro del tiempo de gracia configurado.

**Verificación:** Construir la imagen, arrancar el contenedor con sus variables requeridas,
comprobar logs JSON en stdout y detenerlo verificando el cierre; ejecutar la suite Go
existente.

**Dependencias:** Tarea 1 para elegir contrato de puerto, runtime y apagado.

**Archivos probables:** `Dockerfile`, `.dockerignore`, documentación de ejecución.

**Alcance estimado:** Pequeño/mediano.

## Fase 2: Red y operación

### Tarea 3: Añadir Compose solo para el Orquestador

**Descripción:** Definir una forma de ejecutar/escalar el contenedor Orquestador que se
conecte a la red externa de Traefik y a los servicios downstream, sin incluir un servicio
Traefik ni publicar el puerto de la API directamente al host por defecto.

**Criterios de aceptación:**
- [ ] La configuración Compose contiene únicamente recursos propios del Orquestador.
- [ ] Traefik no aparece como servicio, dependencia, imagen ni proceso a iniciar desde ese
      Compose.
- [ ] La red de entrada de Traefik se referencia como externa y su nombre se configura
      según lo acordado; Compose no la elimina ni la administra.
- [ ] Se pueden iniciar varias réplicas sin `container_name` fijo ni colisión de puertos.
- [ ] URLs, límites y tiempos de espera se configuran desde entorno; no se cambia el
      comportamiento local existente sin documentarlo.

**Verificación:** Validar la configuración Compose y desplegar al menos dos réplicas,
confirmando que cada una puede alcanzar los servicios configurados y que no hay puerto host
fijo ni dependencia de iniciar Traefik desde el stack del Orquestador.

**Dependencias:** Tareas 1 y 2.

**Archivos probables:** `compose.yaml` (o convención existente acordada), documentación.

**Alcance estimado:** Pequeño/mediano.

### Tarea 4: Añadir endpoint de liveness del Orquestador

**Descripción:** Implementar `GET /health` como comprobación de vida local del proceso HTTP.
Debe responder con éxito cuando la instancia atiende HTTP y no debe consultar Extracción ni
Persistencia; la integración con Traefik se configura en la tarea siguiente.

**Criterios de aceptación:**
- [ ] `GET /health` devuelve HTTP 200 sin invocar clientes downstream.
- [ ] El método/path quedan declarados en el router y en el contrato OpenAPI.
- [ ] Los tests cubren la respuesta de liveness y verifican que la ruta no necesita
      configuración ni disponibilidad de downstream.
- [ ] El README deja de describir el health como un POST de procesamiento e indica que
      `/health` es liveness, no readiness.

**Verificación:** `go test ./internal/handler/...` y `go test ./api/...`; ejecutar el
servicio con URLs downstream no disponibles y comprobar que `GET /health` responde 200.

**Dependencias:** Tarea 1 para la decisión de contrato; no requiere Docker ni Traefik.

**Archivos probables:** `internal/handler/router.go`, tests del router, `api/openapi.yaml`,
tests OpenAPI y `README.md`.

**Alcance estimado:** Mediano.

### Tarea 5: Configurar enrutamiento, salud, balanceo y resiliencia con Traefik externo

**Descripción:** Añadir únicamente la configuración asociada al servicio Orquestador para
que la instancia Traefik existente pueda descubrir/rutear sus réplicas y aplicar las
políticas acordadas.

**Criterios de aceptación:**
- [ ] La configuración apunta al servicio HTTP/TCP y al puerto interno real del
      Orquestador; no configura una ruta UDP para tráfico HTTP.
- [ ] La health check de Traefik consulta `GET /health`; no se trata como readiness ni como
      indicador de disponibilidad de Extractor/Persistencia.
- [ ] Una instancia que falle el liveness deja de recibir tráfico conforme al
      comportamiento documentado.
- [ ] El balanceo distribuye requests entre réplicas disponibles.
- [ ] Las políticas de retry/circuit breaker reflejan los límites acordados y no se
      describen como garantía de reejecución en otra instancia si Traefik no la ofrece.
- [ ] Ningún archivo inicia, define o modifica el despliegue global de Traefik.

**Verificación:** Inspeccionar logs/identidad de instancia bajo requests repetidos, detener
una réplica durante tráfico controlado y comprobar que las restantes continúan atendiendo;
confirmar la recuperación de la réplica sin modificar servicios ajenos.

**Dependencias:** Tareas 1, 2, 3 y 4.

**Archivos probables:** Compose/labels del Orquestador y documentación de despliegue.

**Alcance estimado:** Mediano.

## Punto de control: Docker y Traefik

- [ ] Imagen y ejecución local documentadas y reproducibles.
- [ ] Dos o más réplicas balanceadas por la instancia Traefik existente.
- [ ] Caída de una réplica no requiere tocar Extractor, Persistencia ni scripts de carga.
- [ ] Suite Go, build de imagen y validación de Compose satisfactorios.
- [ ] Revisión del usuario antes de proceder a las pruebas de carga.

## Fase 3: Factores y carga

### Tarea 6: Documentar y verificar los 12 factores

**Descripción:** Completar en la documentación del Orquestador una matriz de los 12
factores basada en el comportamiento real del servicio, indicando cumplimiento, evidencia
y cualquier límite o factor no aplicable.

**Criterios de aceptación:**
- [ ] Los 12 factores están documentados explícitamente, con evidencia verificable y no
      afirmaciones genéricas.
- [ ] Se cubren configuración, servicios externos, build/release/run, procesos sin estado,
      binding de puerto, concurrencia, disposability, paridad, logs y tareas administrativas.
- [ ] Se documentan también los factores que no requieren cambios y cualquier brecha
      pendiente, sin alterar servicios ajenos.

**Verificación:** Revisar cada factor contra la imagen, Compose, aplicación y configuración
de runtime; resolver las brechas o registrarlas como pendientes.

**Dependencias:** Tareas 2, 3 y 4.

**Archivos probables:** `README.md`, documentación de despliegue del Orquestador.

**Alcance estimado:** Pequeño/mediano.

### Tarea 7: Validar y ejecutar los archivos de carga de k6 y Vegeta provistos

**Descripción:** Confirmar primero el servicio objetivo de los archivos existentes
`tests/spike_tests.js` y `tests/test_carga.txt`, resolver el acceso a sus PDFs de entrada y
comparar sus requests con el contrato del servicio bajo prueba. Mantener intactos los
originales del profesor. Ejecutar las pruebas solo contra el servicio y endpoint que se
acuerden y registrar los resultados conforme a los criterios del curso.

**Criterios de aceptación:**
- [x] Ambos tests apuntan a `POST /api/v1/documents/process` y envían el archivo en un
      campo multipart llamado `file`.
- [x] Los cuatro PDFs que usa k6 están presentes en el repositorio.
- [x] K6 resuelve los cuatro PDFs bajo `tests/stress/pdfs/` al ejecutarse desde la raíz del
      repositorio.
- [x] Vegeta genera sus cuerpos multipart con `go run ./tests/prepare_vegeta.go`; los
      artefactos generados se excluyen del control de versiones.
- [ ] Los comandos, versiones de k6/Vegeta y criterios/umbrales requeridos quedan
      registrados antes de ejecutar la validación final.
- [ ] La configuración y el número de réplicas usados en cada corrida quedan anotados para
      reproducibilidad.
- [ ] Se reportan resultados de k6 y Vegeta contra una instancia y contra múltiples
      réplicas usando los umbrales confirmados.
- [ ] Se comprueba el comportamiento durante la caída de una réplica si forma parte de los
      requisitos acordados.
- [ ] Se registran fallos/limitaciones explícitamente; no se declara éxito sin comparar con
      los criterios de aceptación.

**Verificación:** Adjuntar o resumir salida, métricas, servicio/URL probado, fixtures,
configuración, versiones y umbrales de ambas herramientas; repetir cualquier corrida no
concluyente. No contar un `404`/error de formato como resultado de rendimiento del
Orquestador.

**Dependencias:** Tareas 1, 3, 4 y 5; Tarea 6 para la documentación final.

**Archivos probables:** `tests/spike_tests.js`, `tests/test_carga.txt`,
`tests/prepare_vegeta.go`, `tests/vegeta_payloads_test.go`, documentación del Orquestador.
Los PDFs originales permanecen intactos.

**Alcance estimado:** Mediano.

## Punto de control final

- [ ] Solo hay cambios dentro del repositorio del Orquestador.
- [ ] La suite y build Go existentes siguen pasando.
- [ ] La imagen y el Compose del Orquestador funcionan sin iniciar Traefik desde ese stack.
- [ ] Los 12 factores están comprobados/documentados.
- [ ] k6 y Vegeta cumplen los umbrales acordados o las brechas están informadas.
- [ ] El usuario revisó la configuración de despliegue y los resultados antes de dar la tarea
      por terminada.
