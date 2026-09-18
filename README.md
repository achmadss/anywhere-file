# anywhere-file

An agent on each of your PCs runs and exposes local applications, starting with dufs.
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
| `qa/` | the failure suite: real processes, one test per row of the table in #40 | Go |
| `packaging/` | a package per operating system, and the scripts that install and reverse it | shell |

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

### The failure suite

`qa/` is what happens when things break. It starts the control plane, an agent and dufs as
real processes, puts a switchboard between the PC and the server, and then cuts the
network, kills the server, runs two agents on one device key and shoots the agent in the
middle of an upload. Each case is one test.

It needs PostgreSQL and dufs on the PATH, and runs only when it is told where the database
is. The schema is created by the suite, so point it at a throwaway:

```sh
RFM_E2E_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable' go test ./qa/
```

Without the variable it does nothing. CI checks that every case ran by name, because a test
that never ran reports the same green tick as one that passed.

## Running it

- The agent, its registry, TLS, discovery, enrolment, the tunnel, the service and the
  packages: [`docs/running-the-agent.md`](docs/running-the-agent.md)
- The control plane against a local PostgreSQL:
  [`docs/running-the-control-plane.md`](docs/running-the-control-plane.md)

## History

This repository first held a peer-to-peer file manager built on iroh, with a Rust core and a
self-hosted relay fleet. That design was replaced in September 2026 by the one in
`docs/new-arch.md`. The old requirements, decision records, spike crates and code are in git
history before the commit that removed them.
