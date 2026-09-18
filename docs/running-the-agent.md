# Running the agent

The agent generates one Ed25519 key per PC on first run and keeps it for the life of the
machine. The server derives `device_id` from the public key, so a replaced key is a new
device and drops the PC out of every binding it had.

```sh
go run ./device/agent key        # print the identity
go run ./device/agent run       # serve the applications and announce this PC on the LAN
go run ./device/agent discover  # list the agents this machine can see on the LAN
go run ./device/agent login https://cloud.example.com  # approve this PC in a browser
go run ./device/agent logout
go run ./device/agent enrol https://cloud.example.com <token>
go run ./device/agent settings  # open the settings page in a browser
go run ./device/agent share add ~/Shared --name files
go run ./device/agent share list
go run ./device/agent share rm files
```

| Variable | Default | What |
|---|---|---|
| `RFM_AGENT_DIR` | the OS config directory, `%LocalAppData%` on Windows | where the agent keeps its own state |
| `RFM_AGENT_ADDR` | `:7433` | the address the LAN gateway listens on, HTTPS |
| `RFM_AGENT_SETTINGS_ADDR` | `127.0.0.1:7434` | the settings endpoint, loopback only, `off` to turn it off |
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

## The application registry

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

## Choosing what this PC shares

`agent.json` can be written by hand, and nobody should have to. The agent serves a settings
page on `127.0.0.1:7434` that lists what this PC shares, walks the filesystem so a folder
can be picked, and removes one. `agent settings` opens it in a browser. A directory chosen
there becomes the dufs entry above, with the port and the command line filled in.

`agent share add`, `share list` and `share rm` do the same from a terminal, which is the
whole of setting up a PC with no screen over ssh.

Both go through the running agent. It holds the registry and builds the gateway's routes
from it, so a command that edited the file itself would leave the agent serving the old
list. A change takes effect at once: the routes are rebuilt, and the application for a
folder that was added is started and the one for a folder that was removed is stopped.
With the agent stopped, `agent share` writes the file directly, because nothing is running
to fall out of step with it.

The endpoint listens on loopback and refuses to start anywhere else. That is not on its own
an authorization, because every account on a shared PC can reach `127.0.0.1`, so it also
takes a token from `settings.token` in the agent's directory, mode 0600. The CLI reads that
file. `agent settings` puts the token in the address it opens, and the page sends it as a
header from then on.

`agent install` writes a `.desktop` entry on Linux, so the page is in the applications menu.
The macOS menu bar item and the Windows tray icon are #142.

## Running an application

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

## TLS on the LAN

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

## Discovery

`agent run` advertises `_anywhere-file._tcp` on the LAN, with the gateway port in the SRV
record and the device id, the display name, the application names and the protocol version
in TXT. A client browses for it and needs no account and no Internet to do so.

The display name is capped at 54 bytes, because it goes in a DNS-SD instance name with a
piece of the device id after it and the whole thing has to fit in 63. Responders drop a
longer one without saying anything, so the agent refuses to start instead.

The client browses for that record, lists what it finds, then reads each PC's discovery
document over the gateway's TLS and shows the name and applications from the document. On
Android it browses through the platform's `NsdManager`. On the desktop it sends the query
from a port of its own every two seconds, the way `agent discover` does. The agent answers
every query by unicast to the port it came from, and a query sent from 5353 gets its answer
delivered to whichever socket on 5353 the kernel picks, usually the OS resolver's. On Android 17 the
system asks the person before an app may look around the local network
(`ACCESS_LOCAL_NETWORK`). The client explains why before asking. When the answer is no, it
offers Android's own picker instead, which shows the PCs the system can see and hands over
the one chosen.

`agent discover` is the same browse from the command line, and is the first thing to run
when a PC does not appear in the client.

## Signing a PC in

A PC works on the LAN with no account. Signing it in adds remote access. The password never
reaches the agent: the browser signs the person in, and the device key proves which PC is
asking (#139, #141, ADR 0006).

`agent login https://cloud.example.com` asks the server for a code, opens a browser at the
address that comes back and waits. The settings page has the same thing behind a button,
and shows the code and the address while it waits. Someone signed in on the website
approves it, and the PC finishes signing itself in.

- The code lives ten minutes and works once.
- A refusal or an expiry is an answer. The agent says which one and stops asking.
- The server address and the device id are written to `agent.json` only after the server
  has accepted. No session and no password is written anywhere.
- A PC with no screen has nothing to open the address with, so the code and the address are
  printed and the approval happens from a phone.

`agent logout` takes the PC off the account. It is signed with the device key, which is why
it needs nobody signed in anywhere: a PC can always remove itself. The bindings are revoked
and remote requests for it stop. What it shares on the LAN is untouched.

The settings endpoint answers `GET /v1/account`, `POST /v1/account/login` and
`POST /v1/account/logout`, behind the same token as the rest of it. The page and the
command both go through the running agent when there is one, for the reason `agent share`
does.

## Enrolment with a token

`agent enrol <server> <token>` is the older path, for a PC set up from a token minted
somewhere else. The failure suite uses it. As above, nothing is written until the server
has accepted, so a bad or expired token leaves the PC as it was.

The gateway no longer takes a POST on `/enrol`. It was there for the client to call, and
#140 removed it once a person could approve a PC in a browser instead. The only ways to
change which account a PC belongs to are the sign in above and `agent enrol`, both run on
the PC itself.

## The tunnel

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

## Running as a service

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

## Installing from a package

`packaging/` builds one package per operating system. Each one places the agent and the dufs
binary, runs `agent install` so the service starts at logon, and reverses both. They land in
`dist/`.

```sh
./packaging/macos/build.sh    # a pkg, universal, for both kinds of Mac
./packaging/linux/build.sh    # a tarball and a deb, amd64 and arm64
./packaging/windows/build.sh  # an MSI, from Git Bash on Windows
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

On Windows the MSI puts both binaries in `Program Files\anywhere-file` and asks for an
administrator, because it also adds the firewall rules: inbound TCP 7433 and UDP 5353, for
the agent alone, from the local subnet, on the Private and Domain profiles. A laptop on a
public network answers nobody. Uninstalling from Settings, Apps removes the rules with it.
Nothing is signed yet, so SmartScreen warns: press More info and then Run anyway.

A service starts with no shell, so settings go into the package when it is built rather than
when it is installed. This is also the only way a package installed by double-clicking can
carry any:

```sh
AGENT_ENV="RFM_AGENT_MDNS=off RFM_AGENT_KEYSTORE=file" ./packaging/linux/build.sh
msiexec /i anywhere-file-0.0.0.msi AGENTENV="RFM_AGENT_MDNS=off"   # or at install time
```

Uninstalling leaves the device key and the agent's directory alone, so a PC that is
reinstalled is the same PC to the server and keeps every binding it had.
