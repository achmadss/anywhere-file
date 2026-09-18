# Packages

One package per operating system. Each one places the agent and the dufs binary, runs
`agent install` so the service starts at logon, and reverses both.

| Script | Makes | Needs |
|---|---|---|
| `macos/build.sh` | `anywhere-file-<version>.pkg`, universal | macOS, Go |
| `linux/build.sh` | a tarball and a deb, amd64 and arm64 | Go, `dpkg-deb` for the deb |
| `windows/build.sh` | `anywhere-file-<version>.msi`, x64 | Windows, Git Bash, Go, dotnet |

Both write into `dist/`. `VERSION` sets the version and defaults to `0.0.0`. The dufs
version is `dufs.version`, and the binary is downloaded from its release page at build time.

## Publishing

Pushing a tag such as `v0.1.0` runs `.github/workflows/release.yml`, which builds the three
packages on their own runners and attaches all of them to a GitHub release under that tag.
The version comes from the tag with the `v` taken off. The website's `/download` page lists
whatever the latest release carries. Nothing else publishes anything: the package job in
`ci.yml` builds and installs a package on every change and then throws it away.

## Opening the settings page

The Linux packages leave a `.desktop` entry behind, written by `agent install`, so "anywhere-file
settings" is in the applications menu and opens the page in a browser. macOS and Windows have
nothing yet (#142); on those, `agent settings` opens it from a terminal.

## Settings in the package

The agent reads its settings from `RFM_AGENT_*` variables, and a service starts with no
shell to read them from, so `agent install` writes whatever is set into the manifest. A
package built with `AGENT_ENV` carries them:

```sh
AGENT_ENV="RFM_AGENT_MDNS=off RFM_AGENT_KEYSTORE=file" ./packaging/linux/build.sh
```

They land in an `agent.env` file beside the binary, and the install script passes each line
to `agent install`. The MSI carries them as a property instead, which can also be set when
it is installed: `msiexec /i anywhere-file.msi AGENTENV="RFM_AGENT_MDNS=off"`. This is how a fleet is built with settings already in it, and the only
way a package installed by double-clicking can carry any. Without it the defaults apply.

## The packages are not signed

Nobody has bought a certificate yet (#131), so both systems say so.

On macOS the pkg is from an unidentified developer. To install it anyway: open it, let the
warning appear, then System Settings, Privacy and Security, scroll to Security and press
Open Anyway. The same panel is where the agent's request to use the local network is
granted, under Privacy, Local Network. Denying it leaves the Mac working and unannounced:
clients have to be given its address rather than finding it.

On Windows SmartScreen warns about an unknown publisher. Press More info, then Run anyway.

On Linux nothing checks a signature, so nothing warns.
