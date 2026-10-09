# anywhere-file

Open the files on your PC from your phone or laptop, at home or away.

## Parts

| Part | Runs on | What it does |
|---|---|---|
| Agent | the PC that holds the files | shares your folders |
| App | your phone or laptop | browses and moves the files |
| Server | any machine you host | signs you in and lets the app reach the PC from away |

## Features

- Find PCs on the LAN with no account and no Internet
- Check a PC holds the key it claims before showing its files
- Browse, send, fetch and zip files
- Remote access with no open port and no fixed address
- Accounts, invitations and per-PC access
- Agent packages for Windows, macOS and Linux
- Apps for Android, Windows, macOS and Linux

## Windows

Get both from the latest [release](https://github.com/achmadss/anywhere-file/releases) or your server's `/download` page.

- Agent: `anywhere-file-<version>.msi` (x64) or `anywhere-file-<version>-arm64.msi`. It installs the agent and dufs, starts the agent at logon, opens its settings page and adds a tray icon.
- App: the desktop app.

The MSI asks for an administrator because it adds firewall rules (TCP 7433 and UDP 5353, local subnet, Private and Domain profiles). Uninstalling from Settings, Apps removes them.

The agent is `agent.exe` in `Program Files\anywhere-file`. The tray icon may sit under the ^ arrow beside the clock. The log is a file in the agent's directory.

## macOS

- Agent: `anywhere-file-<version>.pkg`, for Intel and Apple silicon. It installs `anywhere-file.app` in Applications, starts the agent for whoever is logged in, opens its settings page and adds a menu bar icon.
- App: the desktop app.

Allow Local Network when asked. If you refuse, the Mac serves but does not announce itself, and the app needs its address. You can change it later in System Settings, Privacy and Security, Local Network.

The agent is `/Applications/anywhere-file.app/Contents/MacOS/agent`. To remove everything, run `/Applications/anywhere-file.app/Contents/MacOS/uninstall`. The log is a file in the agent's directory.

## Linux

- Agent: `.deb`, `.rpm` or a tarball, for amd64 and arm64.
- App: the desktop app.

The packages put the agent at `/usr/bin/anywhere-file-agent` and add "anywhere-file settings" to the applications menu. They ship a firewalld service. Open the ports yourself:

```sh
sudo firewall-cmd --permanent --add-service=anywhere-file && sudo firewall-cmd --reload
sudo ufw allow proto tcp to any port 7433   # ufw instead, plus 5353/udp for discovery
```

The tarball needs no root: `./install.sh` puts it under `~/.local`, `./uninstall.sh` removes it.

User services stop at logout unless the account lingers. If the agent does not come back after a reboot:

```sh
sudo loginctl enable-linger $USER
```

Read the log with:

```sh
journalctl --user -u anywhere-file-agent
```

A machine with no Secret Service, such as a NAS or a server, keeps the device key in a file with mode 0600. Only file permissions and disk encryption protect it.

## Android

Install the app: `anywhere-file-<version>.apk` from the latest [release](https://github.com/achmadss/anywhere-file/releases) or your server's `/download` page. There is no agent for Android.

- On the same Wi-Fi the app finds your PCs by itself. On Android 17 and later it asks first to look around the local network. If you say no, it offers Android's own picker.
- For remote access, sign in with your server's address.

## All systems

Each package has a `SHA256SUMS` file on the release to check downloads against. Removing the agent signs the PC out of its account first. The device key stays, so a reinstall is the same PC. One agent serves a PC: a second user on the same PC is asked to sign the first account out and quit anywhere-file there first.

## Use the agent

The commands below call the program `agent`. Its name on each system is in the sections above.

Open the settings page. It shows what the PC shares, the account it is signed in to, and its fingerprint:

```sh
agent settings
```

On macOS and Windows the same page opens from the menu bar or tray icon. On Linux it is in the applications menu.

Share a folder from a terminal:

```sh
agent share add ~/Shared --name files
agent share list
agent share rm files
```

List the PCs this machine can see on the LAN. Run it first when a PC does not show up in the app:

```sh
agent discover
```

Add the PC to your account for remote access, or take it off:

```sh
agent login https://cloud.example.com
agent logout
```

`login` opens a browser. You sign in there and approve the PC. With no screen, it prints a code and an address to open on your phone.

Show the device key, or add and remove the service by hand:

```sh
agent key
agent install
agent uninstall
```

### Settings

Set these as environment variables. A service has no shell, so `agent install` writes them into the service. Change one and run `agent install` again.

| Variable | Default | What |
|---|---|---|
| `RFM_AGENT_DIR` | the OS config directory | where the agent keeps its state |
| `RFM_AGENT_ADDR` | `:7433` | the LAN address, HTTPS |
| `RFM_AGENT_SETTINGS_ADDR` | `127.0.0.1:7434` | the settings page, loopback only, `off` to disable |
| `RFM_AGENT_KEYSTORE` | `auto` | `keyring` for the OS keystore, `file` for a seed file |
| `RFM_AGENT_MDNS` | `on` | `off` where there is no multicast, such as some containers |
| `RFM_AGENT_TUNNEL` | `on` | `off` to stay on the LAN with no outbound connection |
| `RFM_AGENT_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RFM_AGENT_LOG_FILE` | empty | a log file. It rotates at 5 MiB and keeps 3 old files |

To bake settings into a package, set `AGENT_ENV` when you build it. See [`packaging/README.md`](packaging/README.md).

File and folder names are never written to the log.

## Host the server

The server needs PostgreSQL and runs from one binary.

```sh
go build -o control-plane ./hosted/control-plane
export RFM_DATABASE_URL='postgres://user:pass@localhost:5432/rfm'
./control-plane migrate up
./control-plane serve
```

It listens on `:8443`. Check it:

```sh
curl -s localhost:8443/healthz
```

Put it behind HTTPS before real use. Either set `RFM_TLS_CERT` and `RFM_TLS_KEY`, or terminate TLS in a proxy and set `RFM_BASE_URL` to the public address. Without `RFM_BASE_URL` the links in emails carry the proxy's internal address.

| Variable | Default | What |
|---|---|---|
| `RFM_DATABASE_URL` | required | PostgreSQL |
| `RFM_ADDR` | `:8443` | the address to listen on |
| `RFM_TLS_CERT`, `RFM_TLS_KEY` | empty | serve HTTPS directly |
| `RFM_BASE_URL` | from the request | the address that links in emails point to |
| `RFM_SMTP_ADDR` | empty | `host:port` of your mail server. Empty writes each message to the log instead |
| `RFM_SMTP_USER`, `RFM_SMTP_PASSWORD` | empty | mail credentials |
| `RFM_MAIL_FROM` | `no-reply@localhost` | the From address |
| `RFM_OPERATOR_TOKEN` | empty | the token that lets you suspend or restore an account |
| `RFM_OTLP_ENDPOINT` | empty | an OpenTelemetry collector, such as `http://collector:4318`, that receives error reports from PCs |
| `RFM_INSECURE_COOKIES` | empty | for plain HTTP on a local machine only |
| `RFM_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RFM_SHUTDOWN_TIMEOUT` | `20s` | how long to finish requests on shutdown |

The server serves these pages:

| Path | What |
|---|---|
| `/signup` | create an account |
| `/verify` | where the email link lands |
| `/reset`, `/reset/confirm` | reset a password |
| `/approve` | approve a PC that asks to join your account |
| `/download` | the packages on the latest release |

Suspend or restore an account:

```sh
curl -X POST localhost:8443/v1/admin/subscriptions/<account-id> \
  -H "X-Operator-Token: $RFM_OPERATOR_TOKEN" \
  -d '{"status":"suspended"}'
```

Use `"active"` to restore it.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The design notes are in [`docs/`](docs/).

## License

All rights reserved. See [LICENSE](LICENSE).
