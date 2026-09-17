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

The agent is a build target with nothing in it yet, and `client/` does not exist. The work
is broken down in the issue tracker, starting at the
[epic](https://github.com/achmadss/anywhere-file/issues/41).

## Building

The Go code is one module rooted here, so one command builds the agent and the control
plane. The Go version is pinned by the `go` directive in `go.mod`.

```sh
go build ./...
```

Test and lint from the root: `go test ./...`, `go vet ./...`, `go tool staticcheck ./...`,
`gofmt -l .`. CI runs them on ubuntu, macOS and Windows.

## Running the control plane locally

`hosted/control-plane/` needs PostgreSQL. `hosted/control-plane/docker-compose.yml` brings one up on host
port 5433.

```sh
cd hosted/control-plane
docker compose up -d
export RFM_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable'
go run . migrate up          # `migrate down [n]` reverses
go run . seed                # a small development fixture
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
on the ubuntu runner and fails if the concurrency test did not actually run.

## History

This repository first held a peer-to-peer file manager built on iroh, with a Rust core and a
self-hosted relay fleet. That design was replaced in September 2026 by the one in
`docs/new-arch.md`. The old requirements, decision records, spike crates and code are in git
history before the commit that removed them.
