# anywhere-file

A remote file manager for your own devices. You pair a device once, and from then on any
other device you own can browse and move files on it — over the LAN when they share one,
directly across the internet when a hole punch works, and through a relay when it does not.

There is no server holding your files, and no server that can grant access to them. The
control plane distributes signed trust lists and shapes relay bandwidth; it can revoke, but
it can never grant. That distinction is the whole design.

- Requirements: [`remote-file-manager-requirements-r3.md`](docs/remote-file-manager-requirements-r3.md)
- Decisions: [`docs/adr/`](docs/adr/)
- Spike reports: [`docs/spikes/`](docs/spikes/)

## Layout

| Path | What | Stack |
|---|---|---|
| `core/` | `rfm-core` — trust list, shares, access rule, file protocol, transport | Rust |
| `agent/` | desktop agent — hosts `rfm-core` in-process | Rust + Tauri |
| `cloud/` | control plane — trust-list distribution, relay authorization | Go |
| `ui/` | agent UI bundle | TypeScript |
| `console/` | operator console | TypeScript |
| `dashboard/` | customer dashboard | TypeScript |
| `relay/` | pinned `iroh-relay` plus our patch | Rust (vendored) |

## Building

Toolchains are pinned: `rust-toolchain.toml`, the `go` directive in `cloud/go.mod`, `.nvmrc`.
With `rustup`, Go and `nvm` installed, a fresh clone builds with:

```sh
cargo build --workspace          # core + agent
(cd cloud && go build ./...)     # control plane
npm ci && npm run build          # ui + console + dashboard
```

Test and lint the same way: `cargo test --workspace`, `cargo clippy --workspace --all-targets`,
`cargo fmt --check`; `go test ./...`, `go vet ./...`, `go tool staticcheck ./...`;
`npm run typecheck`, `npm run lint`.

The relay is not built by default. See [`relay/README.md`](relay/README.md).

## Running the control plane locally

`cloud/` needs PostgreSQL. `cloud/docker-compose.yml` brings one up on host port 5433.

```sh
cd cloud
docker compose up -d
export RFM_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable'
go run . migrate up          # `migrate down [n]` reverses
go run . seed                # one workspace, two accounts, three devices
go run . serve               # :8443, HTTP unless RFM_TLS_CERT and RFM_TLS_KEY are set
curl -s localhost:8443/healthz
```

The Go tests that touch the schema need the same database, under a separate variable so that
a stray `go test` cannot wipe a development one. They drop and recreate the `public` schema,
so point it at a throwaway:

```sh
RFM_TEST_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable' go test ./...
```

Without it those tests skip, and a skip looks like a pass. CI runs them against a PostgreSQL
service container and fails if the concurrency test did not actually run.

## Status

Pre-implementation. The work is broken down in the issue tracker; start at the
[epic](https://github.com/achmadss/anywhere-file/issues/41).
