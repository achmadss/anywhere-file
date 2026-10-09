# Contributing

## Layout

| Path | What |
|---|---|
| `device/agent/` | the agent, runs on the PC |
| `hosted/control-plane/` | the server: accounts, devices, access, tunnel |
| `internal/` | Go code shared by both |
| `client/` | the app, Kotlin and Compose Multiplatform |
| `qa/` | the failure suite |
| `packaging/` | one package per operating system |

Code under `device/` and `client/` runs on machines we do not control. Treat it as untrusted.

## Build and test

Agent and server. The Go version is pinned by the `go` directive in `go.mod`.

```sh
go build ./...
go test ./...
go vet ./...
go tool staticcheck ./...
gofmt -l .
```

CI runs these on ubuntu, macOS and Windows.

Client. It needs JDK 17 or later. The Android app also needs the Android SDK in `ANDROID_HOME`.

```sh
cd client
./gradlew build                     # both apps, lint and tests
./gradlew :desktopApp:run           # open the desktop app
./gradlew :androidApp:installDebug  # install on a device or emulator
```

## Run the server

It needs PostgreSQL.

```sh
cd hosted/control-plane
docker compose up -d
export RFM_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable'
go run . migrate up
go run . serve
```

More: [`docs/control-plane.md`](docs/control-plane.md)

## Failure suite

`qa/` starts the control plane, an agent and dufs as real processes, then breaks things. It needs PostgreSQL and dufs on the PATH. Point it at a throwaway database:

```sh
RFM_E2E_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable' go test ./qa/
```

Without the variable it skips. A skip looks like a pass, so CI checks every case ran by name.

## Packages

See [`packaging/README.md`](packaging/README.md).

## Docs

- [The agent](docs/agent.md)
- [The control plane](docs/control-plane.md)
- [Requirements](docs/new-arch.md)
- [Decisions](docs/adr/)
- [Threat model](docs/security/threat-model.md)
- [Spike reports](docs/spikes/)
