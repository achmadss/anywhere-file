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

The agent and the control plane are built: a PC holds its device key, serves its
applications on the LAN over TLS, announces itself, keeps a tunnel open to the server,
installs from a package on all three systems, takes the folders it shares from a settings
page or a terminal, and is signed in and out of an account from either. The server also
serves the pages a browser needs, for signup, email verification, password reset, and
approving a PC that asks to join an account, and a download page that lists the packages
on the current release, which a version tag publishes. `client/` builds for Android and the
desktop (#97) and lists the PCs it finds on the LAN (#98); opening what a PC shares is #99
onward. The work is broken down in the issue tracker, starting at the
[epic](https://github.com/achmadss/anywhere-file/issues/41).

## Building

The Go code is one module rooted here, so one command builds the agent and the control
plane. The Go version is pinned by the `go` directive in `go.mod`.

```sh
go build ./...
```

Test and lint from the root: `go test ./...`, `go vet ./...`, `go tool staticcheck ./...`,
`gofmt -l .`. CI runs them on ubuntu, macOS and Windows.

### The client

`client/` is a Gradle build with three modules: `common` holds the UI and networking, and
`androidApp` and `desktopApp` wrap it for each platform. It needs a JDK 17 or later and,
for the Android app, the Android SDK found through `ANDROID_HOME`. The versions are pinned
in `client/gradle/libs.versions.toml` and `client/gradle/wrapper/gradle-wrapper.properties`:

| Tool | Version |
|---|---|
| Gradle | 9.5.0 |
| Kotlin | 2.4.10 |
| Compose Multiplatform | 1.12.0 |
| Android Gradle plugin | 9.2.1 |
| jmdns | 3.6.3 |
| kotlinx.serialization | 1.11.0 |

jmdns is the desktop's mDNS browse. Android browses through the platform's own `NsdManager`.
kotlinx.serialization reads the discovery document.

```sh
cd client
./gradlew build                     # both apps, lint and the tests
./gradlew :desktopApp:run           # opens the window
./gradlew :androidApp:installDebug  # onto the connected device or emulator
```

CI builds both apps on ubuntu. It also starts an agent on the runner, and the desktop tests
look for it: `AgentOnTheLanTest` runs when `RFM_TEST_AGENT_ID` names the agent to expect,
and CI checks that it printed `found agent`. Without the variable it skips.

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
