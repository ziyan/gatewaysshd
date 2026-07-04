# gatewaysshd

[![CI](https://github.com/ziyan/gatewaysshd/actions/workflows/ci.yml/badge.svg)](https://github.com/ziyan/gatewaysshd/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ziyan/gatewaysshd/branch/master/graph/badge.svg)](https://codecov.io/gh/ziyan/gatewaysshd)
[![Release](https://img.shields.io/github/v/release/ziyan/gatewaysshd)](https://github.com/ziyan/gatewaysshd/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`gatewaysshd` is a daemon that provides a meeting place for all your SSH
tunnels. It is especially useful when you have many hard-to-reach machines
running behind firewalls and NAT, and you want to reach services on them over
SSH from anywhere in the world — without opening a single inbound port on
those machines.

## How it works

Machines behind firewalls keep a persistent SSH connection to the gateway and
remote-forward the ports they want to expose, giving each one a service name:

```
workstation$ ssh -T -N workstation@gateway -R ssh:22:localhost:22 -R web:80:localhost:80
```

The gateway does **not** actually open those ports on the server. Forwarded
ports are a virtual concept: the gateway just remembers who advertised what,
and connects the two ends internally when another client asks for the
service. This relieves you of assigning and managing real port numbers on the
server.

From anywhere else, a service is addressed as `service.username`, like a
hostname:

```
laptop$ ssh -T -N username@gateway -L 2222:ssh.workstation:22
laptop$ ssh -p 2222 localhost
```

Authentication is by SSH user certificates only: every client presents a
certificate signed by your certificate authority, and the gateway records the
users, their reported status, and their geolocation in postgres.

## Features

- **Virtual port forwarding** — expose services by name (`web.alice`),
  no server-side port allocation, no inbound ports on the client machines.
- **Certificate-based authentication** — a single CA public key controls who
  can connect; per-user permissions (port forwarding, administrator) are
  carried in the certificate and the database.
- **Built-in shell** — `ssh username@gateway` gives you `status`,
  `listUsers`, `getUser`, `kickUser` (admin), `ping`, and `version` commands.
- **Status reporting** — clients can push arbitrary JSON status and
  screenshots that are stored per user and served over the HTTP API.
- **HTTP API** — `/api/user`, `/api/user/{id}`, `/api/user/{id}/screenshot`
  for dashboards and monitoring.
- **SOCKS5 and HTTP proxies** (optional) — expose a SOCKS5 or HTTP
  forward-proxy port so any client can reach exposed services by name
  (`service.username`), the same reachability as an `ssh -D` dynamic
  forward. Disabled by default; see the security note below.
- **GeoIP** — optionally resolves each user's location from a MaxMind
  database.
- **Mesh peering** — multiple gateway instances share one postgres database
  and form a mesh, so a user on one node can tunnel to a user on another
  node over the same SSH service port. Inter-node trust uses a separate peer
  certificate authority layered on top of each node's existing user CA, so
  existing single-node deployments keep their user-facing CA unchanged.
- **Hardened crypto defaults** — insecure key exchanges, ciphers, and MACs
  are disabled out of the box.

## Installation

Prebuilt binaries for linux, macOS, and windows are on the
[releases page](https://github.com/ziyan/gatewaysshd/releases).

With docker (a distroless image, the binary is the entrypoint):

```
$ docker build -t ziyan/gatewaysshd .
```

Or build from source (Go 1.25+):

```
$ git clone https://github.com/ziyan/gatewaysshd.git
$ cd gatewaysshd
$ make build
```

## Quick start

1. **Create a certificate authority and keys.** Follow [SSH.md](SSH.md) for
   the full walkthrough — generate a CA key, a signed host certificate for
   the gateway, and a signed user certificate for each client.

2. **Start postgres.** The gateway stores users in a postgres database:

   ```
   $ docker run -d --name gatewaysshd-db \
       -e POSTGRES_DB=gatewaysshd \
       -e POSTGRES_USER=gatewaysshd \
       -e POSTGRES_PASSWORD=gatewaysshd \
       -p 5432:5432 postgres
   ```

3. **Run the gateway:**

   ```
   $ gatewaysshd \
       --listen-ssh :2020 \
       --listen-http 127.0.0.1:2080 \
       --ca-public-key id_rsa.ca.pub \
       --host-private-key id_rsa.gateway \
       --host-public-key id_rsa.gateway-cert.pub \
       --geoip-database geoip.mmdb
   ```

   Run `gatewaysshd --help` for the full list of flags, including postgres
   connection settings, idle timeout, and the debug endpoint.

4. **Connect a client:**

   ```
   $ ssh -i ~/.ssh/id_rsa.alice -p 2020 alice@gateway -R web:80:localhost:80
   ```

## The built-in shell

Connecting without a command drops you into a small shell:

```
$ ssh -p 2020 alice@gateway
Welcome to gatewaysshd version 0.4.0! Type "help" to get a list of available commands.
gatewaysshd> status
gatewaysshd> listUsers
gatewaysshd> getUser bob
gatewaysshd> exit
```

`status`, `listUsers`, and `getUser` require the `permit-port-forwarding`
certificate extension; `kickUser` additionally requires the user to be marked
as an administrator in the database.

## SOCKS5 and HTTP proxies

Optionally, the gateway can expose forward proxies so clients that are not
SSH tunnels can still reach exposed services by name. This is the equivalent
of `ssh -D`: the proxy resolves `service.username` (locally or across the
mesh) and bridges to it.

```
$ gatewaysshd \
    ... \
    --listen-socks 127.0.0.1:1080 \
    --listen-http-proxy 127.0.0.1:8080
```

Then, for a service `web` exposed by user `alice`:

```
$ curl --socks5-hostname 127.0.0.1:1080 http://web.alice/
$ curl --proxy http://127.0.0.1:8080 http://web.alice/
```

The HTTP proxy supports both `CONNECT` (tunneling arbitrary TCP) and
absolute-form requests (plain HTTP forwarding).

> **Security:** both proxies are **unauthenticated** — anyone who can reach
> the proxy port can reach any exposed service, bypassing SSH certificate
> auth. They are disabled by default; only enable them bound to a trusted
> network (e.g. `127.0.0.1` or a private interface).

## Mesh peering

Several gateway instances can share a single postgres database and form a
mesh. When a user connects to any node, the node it landed on is recorded on
the user record. When another user asks to tunnel to `service.username`, the
node resolves which node that user is on and forwards the tunnel to that node
over an outbound peer connection — reusing the same SSH service port, no
extra listener.

Inter-node trust is a **separate peer certificate authority**, layered on top
of each node's existing user CA. Peer nodes authenticate as the reserved user
`peer` with a certificate signed by the peer CA (key id = node id); those
certificates are checked only against `--peer-ca-public-key` and never grant
user capabilities. Existing single-node deployments keep their user CA, host
key, and service port unchanged — mesh is enabled purely by adding node
flags:

```
$ gatewaysshd \
    ... \
    --node-id node-us \
    --node-address gateway-us.example.com:2020 \
    --node-certificate id_rsa.node-us-cert.pub \
    --peer-ca-public-key id_rsa.peer-ca.pub
```

Nodes without direct database access can reach the central postgres through a
peer node's SSH service port instead of a separate connection:

```
$ gatewaysshd \
    ... \
    --postgres-peer gateway-us.example.com:2020 \
    --postgres-peer-host-public-key gateway-us.pub \
    --node-certificate id_rsa.node-jp-cert.pub
```

## Development

```
$ make format     # gofmt the tree
$ make build      # build into build/gatewaysshd
$ make test       # run tests (spins up a temporary postgres container)
$ make lint       # golangci-lint
$ make docker     # build the docker image
```

Integration tests need docker: `make test` launches a disposable postgres
container and points the test suite at it via `GATEWAYSSHD_TEST_DATABASE_HOST`.
Without that variable, database-backed tests skip and the rest of the suite
still runs.

## Contributing

- Commit messages follow [conventional commits](https://www.conventionalcommits.org/)
  (`feat: …`, `fix: …`); the release bot derives the next version from them.
- Every pull request description includes a changelog block (pre-filled by
  the PR template). On each release, the bot collects the blocks of all
  merged PRs into [CHANGELOG.md](CHANGELOG.md). Apply the `skip-changelog`
  label if your change has no user-visible effect.
- Releases are fully automated: merging to `master` triggers the release
  bot, which versions, updates the changelog, tags, and publishes binaries.

## License

[MIT](LICENSE)
