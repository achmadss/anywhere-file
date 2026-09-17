# anywhere-file

An agent on each of your PCs runs and exposes local applications, starting with Copyparty.
A client app finds the agent on the LAN and connects to it directly, with no account and no
Internet. Away from home, the same client goes through our server, which checks who you are
and which devices you may reach, then forwards the request down a tunnel the agent keeps
open to it.

- Requirements: [`docs/new-arch.md`](docs/new-arch.md)
- Decisions: [`docs/adr/`](docs/adr/)
- Threat model: [`docs/security/threat-model.md`](docs/security/threat-model.md)
- Spike reports: [`docs/spikes/`](docs/spikes/)

## Layout

Top level splits on where the code runs. `device/` runs on the customer's PC, `client/` on
the customer's phone or laptop, and `hosted/` on hardware we pay for. Anything under
`device/` or `client/` can be tampered with by whoever holds the machine.

| Path | What | Stack |
|---|---|---|
| `device/agent/` | the agent: device identity, mDNS, app registry, local gateway, tunnel client | Go |
| `hosted/control-plane/` | accounts, device registry, authorization, invitations, tunnel endpoint, audit | Go |
| `internal/` | Go shared by the agent and the control plane, starting with request signing | Go |
| `client/` | the client app for Android, Windows, macOS and Linux | Kotlin, Compose Multiplatform |

The agent holds its device key and serves its applications on the LAN so far, and
`client/` does not exist. The work is broken
down in the issue tracker, starting at the
[epic](https://github.com/achmadss/anywhere-file/issues/41).

## Building

The Go code is one module rooted here, so one command builds the agent and the control
plane. The Go version is pinned by the `go` directive in `go.mod`.

```sh
go build ./...
```

Test and lint from the root: `go test ./...`, `go vet ./...`, `go tool staticcheck ./...`,
`gofmt -l .`. CI runs them on ubuntu, macOS and Windows.

## Running the agent

The agent generates one Ed25519 key per PC on first run and keeps it for the life of the
machine. The server derives `device_id` from the public key, so a replaced key is a new
device and drops the PC out of every binding it had.

```sh
go run ./device/agent key        # print the identity
go run ./device/agent run       # serve the applications and announce this PC on the LAN
go run ./device/agent discover  # list the agents this machine can see on the LAN
go run ./device/agent enrol https://cloud.example.com <token>
```

| Variable | Default | What |
|---|---|---|
| `RFM_AGENT_DIR` | the OS config directory, `%LocalAppData%` on Windows | where the agent keeps its own state |
| `RFM_AGENT_ADDR` | `:7433` | the address the LAN gateway listens on |
| `RFM_AGENT_KEYSTORE` | `auto` | `keyring` for the OS keystore, `file` for a seed file |
| `RFM_AGENT_MDNS` | `on` | `off` on a machine with no multicast, such as some containers |
| `RFM_AGENT_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

`auto` uses the OS keystore: Keychain on macOS, Credential Manager on Windows, the Secret
Service on a Linux desktop. A Linux machine with no Secret Service, such as a NAS, a server
or a container, has no keystore, so the seed goes in a mode 0600 file in a mode 0700
directory and wider permissions are refused. On such a machine the device key is protected
by filesystem permissions and by full disk encryption if the operator set one up, and by
nothing else. Set `RFM_AGENT_KEYSTORE` when the guess is wrong.

A keystore that is locked or unreachable is a wait, not a new key. The agent retries and
says so in the log rather than generating an identity that would silently replace the
machine's.

### The application registry

`agent.json` in the agent's directory lists what this PC offers. The agent writes a
starting one on first run.

```json
{
  "name": "pc1",
  "apps": [
    { "name": "copyparty", "type": "http", "address": "127.0.0.1:3923" }
  ]
}
```

The gateway serves each application at `/{name}/` and answers 404 everywhere else. A
request cannot name a host, a port or a scheme: the name is looked up in this file and the
address comes from there, which is what keeps the agent from being an open proxy. The
address stays on the PC and is never sent to the server, which only ever learns the name
and the type.

An application is reached at its own root, so `/copyparty/files/a.txt` arrives as
`/files/a.txt`. An application that writes absolute links has to be told the prefix it is
served under, which for Copyparty is `--rp-loc`.

`GET /.well-known/anywhere-file` returns the device id, the name and the application names,
which is what a client reads after it finds the agent.

### Discovery

`agent run` advertises `_anywhere-file._tcp` on the LAN, with the gateway port in the SRV
record and the device id, the display name, the application names and the protocol version
in TXT. A client browses for it and needs no account and no Internet to do so.

The display name is capped at 54 bytes, because it goes in a DNS-SD instance name with a
piece of the device id after it and the whole thing has to fit in 63. Responders drop a
longer one without saying anything, so the agent refuses to start instead.

`agent discover` is the same browse from the command line, and is the first thing to run
when a PC does not appear in the client.

### Enrolment

A PC works on the LAN with no account. Enrolling it adds remote access: the client mints a
short-lived token for the signed-in account and hands it to the agent, which signs the
enrolment request with its device key and pushes its application list.

The client does this over the LAN by posting to `/enrol` on the gateway. `agent enrol` is
the same thing from a terminal, for a PC with no screen. Either way the server address and
the device id are written to `agent.json` only after the server has accepted, so a bad or
expired token leaves the PC as it was.

## Running the control plane locally

`hosted/control-plane/` needs PostgreSQL. `hosted/control-plane/docker-compose.yml` brings one up on host
port 5433.

```sh
cd hosted/control-plane
docker compose up -d
export RFM_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable'
go run . migrate up          # `migrate down [n]` reverses
go run . serve               # :8443, HTTP unless RFM_TLS_CERT and RFM_TLS_KEY are set
curl -s localhost:8443/healthz
```

The Go tests that touch the schema need the same database, under a separate variable so that
a stray `go test` cannot wipe a development one. They drop and recreate the `public` schema,
so point it at a throwaway:

```sh
RFM_TEST_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable' go test ./...
```

Without it those tests skip, and a skip looks like a pass. CI runs them against PostgreSQL
on the ubuntu runner and fails if the invite race test did not actually run.

## History

This repository first held a peer-to-peer file manager built on iroh, with a Rust core and a
self-hosted relay fleet. That design was replaced in September 2026 by the one in
`docs/new-arch.md`. The old requirements, decision records, spike crates and code are in git
history before the commit that removed them.
