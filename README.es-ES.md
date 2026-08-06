

# gatewaysshd

[![CI](https://github.com/ziyan/gatewaysshd/actions/workflows/ci.yml/badge.svg)](https://github.com/ziyan/gatewaysshd/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ziyan/gatewaysshd/branch/master/graph/badge.svg)](https://codecov.io/gh/ziyan/gatewaysshd)
[![Release](https://img.shields.io/github/v/release/ziyan/gatewaysshd)](https://github.com/ziyan/gatewaysshd/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`gatewaysshd` es un demonio que proporciona un punto de encuentro para todos tus túneles SSH. Es especialmente útil cuando tienes muchas máquinas de difícil acceso detrás de firewalls y NAT, y deseas acceder a los servicios que ejecutan mediante SSH desde cualquier lugar del mundo, sin abrir ni un solo puerto entrante en esas máquinas.

## Cómo funciona

Las máquinas detrás de firewalls mantienen una conexión SSH persistente con la pasarela y reenvían remotamente los puertos que desean exponer, asignando a cada uno un nombre de servicio:

```
workstation$ ssh -T -N workstation@gateway -R ssh:22:localhost:22 -R web:80:localhost:80
```

La pasarela **no** abre realmente esos puertos en el servidor. Los puertos reenviados son un concepto virtual: la pasarela simplemente recuerda qué usuario anunció qué servicio y conecta ambos extremos internamente cuando otro cliente solicita el servicio. Esto te ahorra la tarea de asignar y gestionar números de puerto reales en el servidor.

Desde cualquier otro lugar, un servicio se direcciona como `service.username`, similar a un nombre de host:

```
laptop$ ssh -T -N username@gateway -L 2222:ssh.workstation:22
laptop$ ssh -p 2222 localhost
```

La autenticación se realiza únicamente mediante certificados de usuario SSH: cada cliente presenta un certificado firmado por su autoridad de certificación, y la pasarela registra los usuarios, su estado reportado y su geolocalización en postgres.

## Características

- **Reenvío de puertos virtual** — expone servicios por nombre (`web.alice`), sin asignación de puertos en el servidor, sin puertos entrantes en las máquinas cliente.
- **Autenticación basada en certificados** — una única clave pública de CA controla quién puede conectarse; los permisos por usuario (reenvío de puertos, administrador) se transportan en el certificado y en la base de datos.
- **Shell integrada** — `ssh username@gateway` te proporciona los comandos `status`, `listUsers`, `getUser`, `kickUser` (admin), `ping` y `version`.
- **Informes de estado** — los clientes pueden enviar estados JSON arbitrarios y capturas de pantalla que se almacenan por usuario y se sirven a través de la API HTTP.
- **API HTTP** — `/api/user`, `/api/user/{id}`, `/api/user/{id}/screenshot` para paneles y monitoreo.
- **Proxies SOCKS5 e HTTP** (opcionales) — expone un puerto de proxy directo SOCKS5 o HTTP para que cualquier cliente pueda acceder a los servicios expuestos por nombre (`service.username`), con la misma accesibilidad que un reenvío dinámico `ssh -D`. Desactivados por defecto; consulta la nota de seguridad a continuación.
- **GeoIP** — resuelve opcionalmente la ubicación de cada usuario a partir de una base de datos de MaxMind.
- **Peering en malla** — múltiples instancias de la pasarela comparten una única base de datos postgres y forman una malla, por lo que un usuario en un nodo puede tunelar a un usuario en otro nodo a través del mismo puerto de servicio SSH. La confianza inter-nodo utiliza una autoridad de certificación de pares independiente, superpuesta a la CA de usuario existente de cada nodo, por lo que las implementaciones existentes de un solo nodo mantienen su CA de usuario sin cambios.
- **Valores criptográficos endurecidos** — los intercambios de claves, cifrados y MACs inseguros están desactivados de serie.

## Instalación

Los binarios precompilados para linux, macOS y windows se encuentran en la [página de lanzamientos](https://github.com/ziyan/gatewaysshd/releases).

Con docker (una imagen distroless, donde el binario es el punto de entrada; las imágenes amd64 y arm64 se publican en [Docker Hub](https://hub.docker.com/r/ziyan/gatewaysshd) en cada lanzamiento):

```
$ docker pull ziyan/gatewaysshd
```

O construye la imagen tú mismo:

```
$ docker build -t ziyan/gatewaysshd .
```

O compila desde el código fuente (Go 1.25+):

```
$ git clone https://github.com/ziyan/gatewaysshd.git
$ cd gatewaysshd
$ make build
```

## Inicio rápido

1. **Crea una autoridad de certificación y claves.** Sigue [SSH.md](SSH.md) para la guía completa: genera una clave de CA, un certificado de host firmado para la pasarela y un certificado de usuario firmado para cada cliente.

2. **Inicia postgres.** La pasarela almacena los usuarios en una base de datos postgres:

   ```
   $ docker run -d --name gatewaysshd-db \
       -e POSTGRES_DB=gatewaysshd \
       -e POSTGRES_USER=gatewaysshd \
       -e POSTGRES_PASSWORD=gatewaysshd \
       -p 5432:5432 postgres
   ```

3. **Ejecuta la pasarela:**

   ```
   $ gatewaysshd \
       --listen-ssh :2020 \
       --listen-http 127.0.0.1:2080 \
       --ca-public-key id_rsa.ca.pub \
       --host-private-key id_rsa.gateway \
       --host-public-key id_rsa.gateway-cert.pub \
       --geoip-database geoip.mmdb
   ```

   Ejecuta `gatewaysshd --help` para ver la lista completa de indicadores, incluida la configuración de conexión a postgres, el tiempo de inactividad y el punto de conexión de depuración.

4. **Conecta un cliente:**

   ```
   $ ssh -i ~/.ssh/id_rsa.alice -p 2020 alice@gateway -R web:80:localhost:80
   ```

## La shell integrada

Conectarse sin un comando te introduce en una pequeña shell:

```
$ ssh -p 2020 alice@gateway
Welcome to gatewaysshd version 0.4.0! Type "help" to get a list of available commands.
gatewaysshd> status
gatewaysshd> listUsers
gatewaysshd> getUser bob
gatewaysshd> exit
```

`status`, `listUsers` y `getUser` requieren la extensión de certificado `permit-port-forwarding`; `kickUser` además requiere que el usuario esté marcado como administrador en la base de datos.

## Proxies SOCKS5 e HTTP

De forma opcional, la pasarela puede exponer proxies directos para que los clientes que no sean túneles SSH aún puedan acceder a los servicios expuestos por nombre. Esto equivale a `ssh -D`: el proxy resuelve `service.username` (localmente o a través de la malla) y establece un puente hacia él.

```
$ gatewaysshd \
    ... \
    --listen-socks 127.0.0.1:1080 \
    --listen-http-proxy 127.0.0.1:8080
```

Luego, para un servicio `web` expuesto por el usuario `alice`:

```
$ curl --socks5-hostname 127.0.0.1:1080 http://web.alice/
$ curl --proxy http://127.0.0.1:8080 http://web.alice/
```

El proxy HTTP admite tanto `CONNECT` (túneles TCP arbitrarios) como solicitudes en forma absoluta (reenvío HTTP puro).

> **Seguridad:** ambos proxies son **no autenticados** — cualquiera que pueda acceder al puerto del proxy podrá alcanzar llegar a cualquier servicio expuesto, eludiendo la autenticación por certificados SSH. Están desactivados por defecto; actívalos únicamente si están enlazados a una red de confianza (p. ej., `127.0.0.1` o una interfaz privada).

## Peering en malla

Varias instancias de la pasarela pueden compartir una única base de datos postgres y formar una malla. Cuando un usuario se conecta a cualquier nodo, se registra el nodo al que se conectó en el registro del usuario. Cuando otro usuario solicita tunelar a `service.username`, el nodo resuelve en qué nodo se encuentra ese usuario y reenvía el túnel a dicho nodo a través de una conexión de par saliente, reutilizando el mismo puerto de servicio SSH, sin /escucha adicional.

La confianza inter-nodo es una **autoridad de certificación de pares independiente**, superpuesta a la CA de usuario existente de cada nodo. Los nodos par se autentican como el usuario reservado `peer` con un certificado firmado por la CA de pares (id de clave = id de nodo); estos certificados se verifican únicamente frente a `--peer-ca-public-key` y nunca otorgan capacidades de usuario. Las implementaciones existentes de un solo nodo mantienen su CA de usuario, clave de host y puerto de servicio sin cambios; la malla se activa puramente añadiendo los indicadores de nodo:

```
$ gatewaysshd \
    ... \
    --node-id node-us \
    --node-address gateway-us.example.com:2020 \
    --node-certificate id_rsa.node-us-cert.pub \
    --peer-ca-public-key id_rsa.peer-ca.pub
```

Los nodos sin acceso directo a la base de datos pueden acceder a la postgres central a través del puerto de servicio SSH de un nodo par, en lugar de una conexión separada:

```
$ gatewaysshd \
    ... \
    --postgres-peer gateway-us.example.com:2020 \
    --postgres-peer-host-public-key gateway-us.pub \
    --node-certificate id_rsa.node-jp-cert.pub
```

## Desarrollo

```
$ make format     # gofmt the tree
$ make build      # build into build/gatewaysshd
$ make test       # run tests (spins up a temporary postgres container)
$ make lint       # golangci-lint
$ make docker     # build the docker image
```

Las pruebas de integración necesitan docker: `make test` inicia un contenedor desechable de postgres y dirige la suite de pruebas hacia él mediante `GATEWAYSSHD_TEST_DATABASE_HOST`. Sin esa variable, las pruebas que requieren base de datos se omiten y el resto de la suite se ejecuta normalmente.

## Contribución

- Los mensajes de commit siguen [conventional commits](https://www.conventionalcommits.org/) (`feat: …`, `fix: …`); el bot de lanzamiento extrae la próxima versión a partir de ellos.
- La descripción de cada pull request incluye un bloque de registro de cambios (prellenado por la plantilla del PR). En cada lanzamiento, el bot recopila los bloques de todos los PRs fusionados en [CHANGELOG.md](CHANGELOG.md). Aplica la etiqueta `skip-changelog` si tu cambio no tiene un efecto visible para el usuario.
- Los lanzamientos están completamente automatizados: fusionar a `master` activa el bot de lanzamiento, que asigna la versión, actualiza el registro de cambios, crea las etiquetas y publica los binarios.

## Licencia

[MIT](LICENSE)
