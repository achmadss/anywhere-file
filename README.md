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

The relay is not built by default — see [`relay/README.md`](relay/README.md).

## Status

Pre-implementation. The work is broken down in the issue tracker; start at the
[epic](https://github.com/achmadss/anywhere-file/issues/41).
