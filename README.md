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
| `RFM_AGENT_ADDR` | `:7433` | the address the LAN gateway listens on, HTTPS |
| `RFM_AGENT_KEYSTORE` | `auto` | `keyring` for the OS keystore, `file` for a seed file |
| `RFM_AGENT_MDNS` | `on` | `off` on a machine with no multicast, such as some containers |
| `RFM_AGENT_TUNNEL` | `on` | `off` to keep the PC on the LAN only, with no outbound connection |
| `RFM_AGENT_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RFM_AGENT_LOG_FILE` | empty | a file to append the log to, instead of standard error |

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

`agent.json` in the agent's directory lists what this PC offers. The agent writes an empty
one on first run.

```json
{
  "name": "pc1",
  "apps": [
    {
      "name": "files",
      "type": "http",
      "address": "127.0.0.1:5000",
      "command": ["dufs", "/Users/ana/Shared", "--bind", "127.0.0.1", "--port", "5000",
                  "--path-prefix", "/files", "--allow-all"]
    }
  ]
}
```

The gateway serves each application at `/{name}/` and answers 404 everywhere else. A
request cannot name a host, a port or a scheme: the name is looked up in this file and the
address comes from there, which is what keeps the agent from being an open proxy. The
address stays on the PC and is never sent to the server, which only ever learns the name
and the type.

The name stays on the path, so `/files/holiday/a.txt` reaches the application with the
`/files` still on it. An application has to be told the prefix it is served under anyway,
or the links it writes land nowhere, and one that is told strips the prefix itself. dufs is
told with `--path-prefix`.

### Running an application

An entry with a `command` is an application the agent runs. It starts it, starts it again
when it exits, waiting a second and doubling to a minute, and stops it when the agent
stops. Whatever the application writes goes to the agent's log under the application's
name, because a service has no window to write it to.

The command runs as written. The agent fills nothing in, so the port appears twice: once in
the command the application listens with, once in the address the gateway dials. An entry
with no command is an application something else starts, which the agent only forwards to.

The program is looked for on the PATH and then next to the agent's own binary. A service
starts with the system's PATH and none of yours, so `dufs` in the registry finds the dufs a
package installed beside the agent without anything being said about where it is.

The address of an application the agent starts has to be a loopback one. An application
listening on the LAN can be reached without going through the gateway at all, so everything
the gateway refuses would be reachable around it.

`GET /.well-known/anywhere-file` returns the device id, the name and the application names,
which is what a client reads after it finds the agent.

### TLS on the LAN

The gateway serves HTTPS with a certificate it signs itself. There is no authority to check
it against, so the device key is what a client trusts instead. The discovery document
carries the device's public key and that key's signature over the certificate's public key,
so a client:

1. checks that the digest of the public key is the `device_id` it was looking for,
2. checks that the signature covers the certificate the connection is actually using.

Another PC can copy the document and cannot serve a certificate to match it. mDNS carries
no key and is not trusted for one.

The certificate holds a P-256 key derived from the device key, one per PC and the same
after every restart, so a client may pin it as well. It is a derived key because an
Ed25519 certificate is refused outright by Schannel on Windows, by LibreSSL on macOS and
by browser engines. The agent replaces the certificate a month before it runs out.

It names the device `<device_id>.anywhere-file` and covers this machine's addresses, so a
client that checks the name against the address it dialled finds it there. By hand,
`curl -k https://localhost:7433/.well-known/anywhere-file` is the whole document.

Applications stay on plain HTTP on loopback behind the gateway. What crosses the network is
the gateway's connection.

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

### The tunnel

An enrolled PC keeps one outbound connection to the server open, so remote access needs no
inbound port, no port forwarding and no fixed address. The agent signs the request that
opens it, the server answers by handing the connection over, and from then on the server
sends requests down it and the same gateway answers them. Nothing the LAN cannot reach is
reachable this way either.

The connection drops whenever the network does. The agent dials again, waiting a second
and doubling to a minute, with the wait spread out so a server coming back does not take
every agent it dropped in the same instant. An unenrolled PC does the same until it is
enrolled, and being offline is logged rather than treated as a failure.

The server sends a ping every 30 seconds. A connection that carries nothing for 90 seconds
is treated as gone from the agent's side too, because a broken path can leave a socket
looking open for minutes.

### Running as a service

```sh
go build -o agent ./device/agent
./agent install
```

`agent install` hands the binary to whatever starts programs on this OS and asks for it
back after a reboot: a launchd agent on macOS, a logon-triggered scheduled task on
Windows, a systemd user unit on Linux. Each one restarts the agent if it exits.

It installs into the user's own session everywhere, never machine wide. The device key
lives in the user's keystore and a machine-wide service cannot read it. On Windows that
rules out a real service, which runs in session 0 with no access to the user's credentials.

A service starts with no shell, so the `RFM_AGENT_*` variables set when `install` runs are
written into the manifest as arguments: `agent run RFM_AGENT_MDNS=off`. The command line is
the one place all three schedulers agree on. Change a variable and install again.

The log goes where each OS looks for it: a file next to the agent's state on macOS and
Windows, the journal on Linux (`journalctl --user -u anywhere-file-agent`).

On Linux the user's services stop at logout unless the account lingers. `install` asks for
lingering and carries on with a warning if it is refused, which leaves an agent that runs
now and does not come back after a reboot. `sudo loginctl enable-linger $USER` fixes it.

`agent uninstall` removes the manifest and stops the service. The device key, the registry
and the log stay where they are, so reinstalling gets the same device back.

### Installing from a package

`packaging/` builds one package per operating system. Each one places the agent and the dufs
binary, runs `agent install` so the service starts at logon, and reverses both. They land in
`dist/`.

```sh
./packaging/macos/build.sh    # a pkg, universal, for both kinds of Mac
./packaging/linux/build.sh    # a tarball and a deb, amd64 and arm64
```

On macOS the pkg installs `anywhere-file.app` into `/Applications` and starts the service for
whoever is logged in. Nothing is signed yet, so macOS calls it an unidentified developer and
refuses to open it on the first try. Open it anyway: System Settings, Privacy and Security,
scroll down to Security, press Open Anyway, then open the pkg again. The first run also asks
whether the agent may use the local network, and the answer is kept in the same panel under
Privacy, Local Network. Saying no leaves the Mac serving and unannounced, so a client has to
be given its address rather than finding it by itself.
`/Applications/anywhere-file.app/Contents/MacOS/uninstall` takes it all away again.

On Linux the deb puts both binaries in `/usr/lib/anywhere-file`, links the agent into
`/usr/bin` as `anywhere-file-agent`, and ships a firewalld service file. Opening the ports is
left to whoever runs the machine:

```sh
sudo firewall-cmd --permanent --add-service=anywhere-file && sudo firewall-cmd --reload
sudo ufw allow proto tcp to any port 7433   # ufw instead, plus 5353/udp for discovery
```

The tarball is the same thing under `~/.local` with no root anywhere: `./install.sh` to put
it there, `./uninstall.sh` to take it away.

A service starts with no shell, so settings go into the package when it is built rather than
when it is installed. This is also the only way a package installed by double-clicking can
carry any:

```sh
AGENT_ENV="RFM_AGENT_MDNS=off RFM_AGENT_KEYSTORE=file" ./packaging/linux/build.sh
```

Uninstalling leaves the device key and the agent's directory alone, so a PC that is
reinstalled is the same PC to the server and keeps every binding it had.

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
