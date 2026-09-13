# Spike: Tauri desktop shell, in-process iroh, and a user-level service

Issue [#4](https://github.com/achmadss/anywhere-file/issues/4). Requirements r3 §20.4, §13.1, D2, D4.

## The question

Can a Tauri shell host `rfm-core` in the same process on macOS, Windows and Linux: one window,
one iroh endpoint with a live accept loop, one async runtime shared cleanly, a bulk data path to
the webview that does not buffer a multi-GB file, and a user-level service that keeps answering
peers when the window is closed.

## Honesty about coverage

Only one machine was available: macOS 15.7.3 (Darwin 24.6.0, build 24G419) on arm64.
There was no Windows machine and no Linux desktop. Every claim below carries one of four labels.

| Label | Meaning |
|---|---|
| OBSERVED | Ran here on macOS arm64. The output is quoted. |
| CONTAINER | Ran in a Linux container. A container shares the host kernel and has no desktop session, so it proves that code compiles and that libraries and protocols behave. It proves nothing about a real Linux desktop. |
| READ | From the crate source, vendor docs or an issue tracker, cited by `file:line` or URL. |
| EXPECTED | Reasoning from the above. Could be wrong. |

The Windows column of this report is entirely READ and EXPECTED. Nothing in it has been run.

## What was built

`spikes/desktop-shell/`, its own Cargo workspace so the root `cargo build --workspace` never
pulls Tauri or iroh in. Three binaries and three scripts.

| Path | What it does |
|---|---|
| `src/main.rs` | `shell`: Tauri window, IPC commands, in-process iroh endpoint plus accept loop, `rfmfile://` uri-scheme handler, hand-rolled localhost HTTP streamer |
| `src/bin/headless.rs` | `headless`: iroh endpoint and accept loop with no window at all. This is what launchd keeps alive |
| `src/bin/probe.rs` | `probe`: a second process that dials a published endpoint address and echoes a payload. Stands in for a peer |
| `bench.sh` | Runs a scripted transfer and samples the process RSS from outside every 250 ms |
| `launchd/install.sh`, `launchd/uninstall.sh` | Generate, bootstrap and remove a per-user launchd agent |

Versions resolved: `tauri 2.11.5`, `wry 0.55.1`, `tao 0.35.3`, `iroh 1.2.0`, `tokio 1.53.1`,
`rustc 1.98.1`. The lockfile is committed so the measurements can be reproduced.

The window is scriptable from a terminal. `SPIKE_RUN=list,http,proto` puts a query string on the
window URL and the page runs those steps on load, reporting each step back through an IPC command
so it lands in the same log as the memory samples.

## 1. Window plus a Rust command the JS bundle invokes

OBSERVED. Working end to end.

```
[http] listening on 127.0.0.1:63227
[iroh] endpoint id 224556b4e85bb3d74c973aa481daa0b1e5216256da7e2e545ccd9a5a4ca820f1
[win] visible=Ok(true) inner=Ok(PhysicalSize { width: 980, height: 760 }) outer_pos=Ok(PhysicalPosition { x: 790, y: 189 }) scale=Ok(1.0)
[mark] webview-alive ua=Mozilla/5.0 (Macintosh; Intel Mac OS X 1 viewport=980x732 dpr=1 bytes=0 rss_kb=89360
[mark] list_dir /private/tmp/.../scratchpad -> 54 entries bytes=0 rss_kb=89008
```

The `[win]` line is Tauri reporting the real window it created. The `[mark]` lines come from
JavaScript calling back into Rust, so the round trip works in both directions. `list_dir` is the
directory listing the acceptance criteria asked for.

A screenshot was attempted and is not available. `screencapture` on this machine returns the
desktop picture with every window missing, because the terminal has not been granted Screen
Recording permission. The macOS menu bar in that capture does read `rfm-desktop-shell-spike`,
which confirms the process registered with the WindowServer as the active GUI application, but it
is weaker evidence than the geometry line above, so the geometry line is what this report relies
on.

The binary is unbundled. It runs from `target/release/shell` with no `.app` wrapper, which is fine
for a spike and is not how it would ship.

## 2. Runtime ownership

OBSERVED, and READ for the mechanism.

Two things own two different resources, and they do not contend for either.

The `tao` AppKit event loop owns the main thread. `tauri::Builder::run()` never returns. It is not
an async runtime and it does not want one.

Tokio owns its own worker threads. We build that runtime in `main` and hand the handle to Tauri
before anything else happens:

```rust
let rt = tokio::runtime::Builder::new_multi_thread().enable_all().build().unwrap();
tauri::async_runtime::set(rt.handle().clone());
```

From then on every `tauri::async_runtime::spawn` lands on our runtime, and that includes iroh's
endpoint, its accept loop, the localhost streamer and the uri-scheme handler. One runtime, one set
of worker threads.

Two constraints from the source, both worth knowing before this pattern goes into `agent/`:

- If you never call `set`, Tauri lazily builds its own multi-threaded tokio runtime on first use
  (`tauri-2.11.5/src/async_runtime.rs:222`). Spawning iroh on a separate runtime of your own would
  then leave two runtimes in the process, doubling the thread pool and splitting shutdown.
- `set` panics if the global runtime has already been initialised
  (`tauri-2.11.5/src/async_runtime.rs:255`). It has to be the first thing in `main`, before any
  Tauri call that might touch `handle()`, `spawn()` or `block_on()`.

Tauri stores the handle without taking ownership (`runtime: None` in the same function), so the
caller has to keep the `Runtime` value alive. Holding it in `main` is enough, since `run()` does
not return.

Coexistence was tested under load rather than at idle. Twelve alternating 5 GiB passes, 60 GiB
through the webview in total, with a `probe` process dialling the window's own endpoint partway
through:

```
[mark] protocol-done 2.7s 2000MB/s bytes=5368709120 rss_kb=110096
[mark] ui-heartbeat bytes=18 rss_kb=61488
[iroh] accepted connection from c1dc5507cd688446e4e1a1f4b71d21ce7181b8a4e81c43bbbe332857becc433f
[iroh] echoed 14 bytes back to c1dc5507cd688446e4e1a1f4b71d21ce7181b8a4e81c43bbbe332857becc433f
[mark] http-done 2.3s 2342MB/s bytes=5368709120 rss_kb=47408
```

```
[probe] dialling c2d0ff4d2790c3d514e26e7ada2d77a25edacc0f568c6ac7c15352967dad4526
[probe] ECHO OK in 1.042414584s: "is anyone home"
probe exit=0
```

The `ui-heartbeat` marks come off a 1 second `setInterval` in the page. They fired at ticks
3, 6, 9 through 48 with no gap, including through every transfer, so the webview main thread was
never starved. The iroh accept happened between pass six and pass seven while the transfer loop
was still running.

The window displays the endpoint id live, refreshed from Rust once a second, which is the
"both are alive at once" proof the issue asked for.

CONTAINER: the same source compiles for Linux. `rust:1.98.1-bookworm` on aarch64 with
`libwebkit2gtk-4.1-dev` (WebKitGTK 2.50.6), `libsoup-3.0-dev`, `libjavascriptcoregtk-4.1-dev`,
`libxdo-dev`, `libayatana-appindicator3-dev`, `librsvg2-dev` produces all three binaries:

```
Finished `release` profile [optimized] target(s) in 1m 42s
target/release/shell: ELF 64-bit LSB pie executable, ARM aarch64 ... for GNU/Linux 3.7.0
```

That establishes the Linux system-package list and that nothing in the code is macOS-specific at
compile time. It says nothing about whether the window opens, because the container has no
display server.

## 3. Streaming a multi-GB file to the webview

This is where the interesting result is. Tauri's uri-scheme protocol is not a streaming interface
on any platform, and the source says so plainly.

`UriSchemeResponder::respond` takes `http::Response<T> where T: Into<Cow<'static, [u8]>>`
(`tauri-2.11.5/src/app.rs:2462`). Whatever you answer with is already a complete buffer in memory
before the webview sees a byte. Underneath, every backend copies it again:

| Platform | wry code | What it does |
|---|---|---|
| macOS | `wry-0.55.1/src/wkwebview/class/url_scheme_handler.rs:271` | `NSData::initWithBytes_length` over the whole body, then `didReceiveData` |
| Windows | `wry-0.55.1/src/webview2/mod.rs:1127` | `SHCreateMemStream` over the whole body |
| Linux | `wry-0.55.1/src/webkitgtk/web_context.rs:223` | `MemoryInputStream::from_bytes` over the whole body |

READ, all three. Tauri's own `asset:` protocol works around this by capping a single range
response at `const MAX_LEN: u64 = 1000 * 1024` (`tauri-2.11.5/src/protocol/asset.rs:118`), which
is the same trick this spike uses at a larger chunk size.

So there are two workable mechanisms, and both were measured on a real 5 GiB file
(5,368,709,120 bytes, `mkfile 5g`, not sparse).

### Mechanism A: `rfmfile://` uri-scheme handler with HTTP Range

The handler refuses to answer with more than 8 MiB at a time and the page walks the file with
`Range` headers, dropping each `ArrayBuffer` before requesting the next.

OBSERVED, 5 GiB, RSS sampled every 250 ms from outside the process:

```
elapsed_s  rss_kb
0          4464      (before the window exists)
1          97456
1          147504
2          159808    <- peak
3          126512
4          110480
5          118224
6          118272
7          118320
```

```
[mark] protocol-done 3.1s 1755MB/s bytes=5368709120 rss_kb=129328
```

Peak 156 MiB, settling near 115 MiB. Flat: 5 GiB moved through a process that never held more
than about a tenth of a GiB.

### Mechanism B: a localhost HTTP server

A `tokio::net::TcpListener` on `127.0.0.1:0`, one `Content-Length` response, the file copied to
the socket through a fixed 256 KiB buffer. The page drains it with a `ReadableStream` reader. No
HTTP framework, roughly forty lines.

OBSERVED, 5 GiB:

```
elapsed_s  rss_kb
0          9600
0          89184     <- peak
1          82144
1          80384
2          79760
2          71040
2          70928
```

```
[mark] http-done 2.6s 2080MB/s bytes=5368709120 rss_kb=70016
```

Peak 87 MiB, ending at 69 MiB. Lower than mechanism A and about twice as fast, because there is
one buffer in flight rather than an 8 MiB `Vec` plus an 8 MiB `NSData` plus an 8 MiB `ArrayBuffer`
churning 640 times.

### The counterfactual

A flat memory curve is only meaningful if the same harness can be made to produce a bad one.
`SPIKE_NO_CAP=1` removes the chunk cap so a single unranged GET answers with the whole file. Run
against a 1 GiB file, because doing it at 5 GiB would have taken the machine down:

```
elapsed_s  rss_kb
0          3792
0          86624
0          626368
0          1775440
1          2318176   <- peak, 2.21 GiB for a 1 GiB file
1          72864
1          74128
```

Roughly 2.2x the file size, which is the Rust `Vec` plus the `NSData` copy, exactly as the wry
source predicts. Extrapolated to 5 GiB that is about 11 GiB resident. The cap is doing the work,
and the measurement is real rather than an artefact of the harness.

### Sustained

Twelve passes, 60 GiB total, RSS across 99 samples: minimum 42.8 MiB, median 78.3 MiB,
maximum 163.2 MiB. Memory does not track bytes transferred.

### Per-OS verdict on the data path

macOS is OBSERVED. Windows and Linux are EXPECTED, from the wry source above, with one caveat
each.

The uri-scheme mechanism should behave the same everywhere, because the buffering is in the
shared `UriSchemeResponder` type and every backend copies whole buffers. One difference to handle:
the custom scheme is addressed as `rfmfile://localhost/` on macOS and Linux, and rewritten to
`http://rfmfile.localhost/` on Windows and Android
(`tauri-2.11.5/src/webview/mod.rs:2388`). The spike branches on the user agent.

The localhost mechanism has a mixed-content question outside macOS. On Windows the page origin is
`http://tauri.localhost`, so plain HTTP to `127.0.0.1` is same-scheme and should pass, unless
`useHttpsScheme` is turned on (`tauri-2.11.5/src/webview/mod.rs:1093`), which would make it a
secure origin fetching an insecure one. On Linux the page origin is the custom `tauri://` scheme
and WebKitGTK's treatment of it is untested here. Both are EXPECTED and both are cheap to check
once a machine exists.

Recommendation: use the localhost server for bulk file bodies and keep the uri-scheme handler for
thumbnails, previews and anything a media element will range-request on its own. Bind the listener
to `127.0.0.1` on an ephemeral port and gate it with a per-launch token in the URL, since any
local process can reach a loopback port.

## 4. User-level service plus a windowed app (r3 §13.1)

### macOS: OBSERVED, works

A per-user launchd agent at `~/Library/LaunchAgents/dev.anywherefile.spike.headless.plist`,
bootstrapped into the `gui/$UID` domain. No root, no admin prompt, no installer.

```
gui/501/dev.anywherefile.spike.headless = {
	active count = 1
	path = /Users/achmad/Library/LaunchAgents/dev.anywherefile.spike.headless.plist
	type = LaunchAgent
	state = xpcproxy
```

The agent binds its endpoint and publishes its address:

```
[headless] pid 39896 endpoint id 9442a214c3d543d8f9e4617618b8c609f31cd6501cb431354e87119d0abd80d5
[headless] online, wrote /tmp/rfm-spike-headless.json
```

With no window process running at all (`pgrep -fl 'release/shell'` returned nothing), a second
process dialled it:

```
[probe] dialling 9442a214c3d543d8f9e4617618b8c609f31cd6501cb431354e87119d0abd80d5
[probe] ECHO OK in 1.009754834s: "is anyone home"
exit=0
```

Then with the window open, and again after quitting it:

```
=== probe service WITH window open ===
[probe] ECHO OK in 4.828208ms: "is anyone home"
=== probe service WITH NO WINDOW ===
gui alive?
none - window gone
[probe] ECHO OK in 4.772459ms: "is anyone home"
```

`KeepAlive` was tested with `kill -9`. launchd respawned the agent inside six seconds:

```
=== after kill -9 ===
	state = running
	pid = 40255
```

Two findings fall out of that respawn, and both change other issues.

The endpoint id changed across the restart, from `9442a2…` to `e38251…`, because the spike calls
`SecretKey::generate` on every boot. A device identity that changes when a service restarts is
useless to a trust list. The service must load a persisted secret key at startup, which is
issue #2's keystore, and #2 is now a hard blocker for anything that pairs devices.

The window process and the agent each bound their own endpoint, so this machine had two identities
at once. That is fine for a spike and wrong for the product. Exactly one process should hold the
device identity, and it has to be the service, since it is the one that outlives the window.

### macOS service model chosen

The agent holds the endpoint and owns the device identity. The window is a client of it, over a
local IPC channel, and holds no endpoint of its own. `RunAtLoad` plus `KeepAlive` plus
`ProcessType Background`. The plist is generated by the installer rather than shipped verbatim,
because it needs an absolute path to the installed binary.

The open piece is the local IPC between window and agent, which this spike did not build. r3 §7
says the UI bundle must not open a socket, and it does not have to: the window process can talk to
the agent over a Unix domain socket in the user's container and expose it to the bundle through
Tauri IPC commands, which keeps the socket in Rust where §7 wants it.

### Windows: READ only, nothing run

A Windows service in the SCM sense is the wrong tool. Services run in session 0 and
"cannot directly interact with a user as of Windows Vista"
([Microsoft Learn, Interactive Services](https://learn.microsoft.com/en-us/windows/win32/services/interactive-services)).
The same page describes the supported shape as a service plus a separate GUI process launched into
the user's session that talks to it over IPC, which is structurally the model chosen for macOS.
Installing an SCM service also needs administrator rights, and the service would run as SYSTEM or
a service account rather than as the user, which breaks per-user file access.

A Scheduled Task with a logon trigger is the closer match. Task Scheduler event-based triggers can
start a task "when a user logs on to the local computer"
([Microsoft Learn, Task Triggers](https://learn.microsoft.com/en-us/windows/win32/taskschd/task-triggers)),
which gives a per-user process without admin rights. Restart-on-failure is a task setting rather
than the always-on supervision `KeepAlive` provides, so EXPECTED: the agent will need its own
watchdog behaviour or a short repetition interval.

To actually test this: a Windows 11 machine or VM with WebView2 present, `schtasks /create /tn ...
/sc onlogon`, then run `probe` from a second process with the window closed. Half a day including
setup.

### Linux: READ only, nothing run on a desktop

A systemd user unit in `~/.config/systemd/user/` with `systemctl --user enable --now`. For the
agent to survive logout, the user needs lingering: `loginctl enable-linger` means "a user manager
is spawned for the user at boot and kept around after logouts", which "allows users who are not
logged in to run long-running services"
([loginctl(1)](https://man7.org/linux/man-pages/man1/loginctl.1.html)). `Restart=always` is the
`KeepAlive` equivalent.

This is READ, not CONTAINER, deliberately. A container has no logind session and usually no
systemd at all, so running a unit file there would prove nothing about the mechanism that matters.
Saying "we tested it in Docker" here would be the false certainty this spike is meant to avoid.

To actually test this: a Linux desktop VM (GNOME or KDE) with `libwebkit2gtk-4.1`, install the
unit, `enable-linger`, log out, then `probe` over SSH. Half a day. The distro package matrix for
WebKitGTK is the part likely to eat the time, not systemd.

## 5. Code signing and notarization, sized not done

### macOS

Tooling is already on this machine. OBSERVED: `xcrun --find notarytool` resolves to
`/Library/Developer/CommandLineTools/usr/bin/notarytool` and `stapler` sits beside it, so full
Xcode is not required. `codesign` at `/usr/bin/codesign` ad-hoc signs the built binary:

```
Identifier=shell-adhoc-55554944386df3f9dee8362a8d735eae6c125203
Format=Mach-O thin (arm64)
Signature=adhoc
TeamIdentifier=not set
```

What is missing is the certificate. `security find-identity -v -p codesigning` reports
`0 valid identities found`. A Developer ID Application certificate signs a Mac app "before
distributing it outside the Mac App Store" and only an Account Holder or Admin on an enrolled team
can create one ([Apple, Certificates](https://developer.apple.com/support/certificates/)), so the
$99/year Apple Developer Program enrolment is the gate.

Tauri already has the configuration surface: `signing_identity`, `hardened_runtime` (default
`true`), `entitlements` and `provider_short_name` at `tauri-utils-2.9.3/src/config.rs:653-661`.

Sizing: one day of engineering once enrolment completes, plus enrolment latency, which for an
organisation means a D-U-N-S number and can take a week or more of waiting. Hardened runtime plus
notarytool submission plus stapling is well-trodden. Budget an extra day for the entitlements
iteration, since the agent binds sockets and reads user files and each rejection is a round trip.

### Windows

Authenticode. The cost has changed since 2023 and the change matters for CI design. Code signing
private keys issued after 1 June 2023 must be generated and held in hardware meeting FIPS 140-2
level 2 or Common Criteria EAL 4+, non-exportable, for both OV and EV certificates
([CA/Browser Forum requirements summary](https://www.encryptionconsulting.com/understanding-the-ca-browser-forum-code-signing-requirements/)).
A USB token cannot sit in a hosted CI runner, so the practical routes are a cloud signing service
such as Azure Trusted Signing
([Microsoft Learn, code signing options](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options))
or a self-hosted runner with the token attached.

Tauri supports both shapes: `certificate_thumbprint`, `digest_algorithm`, `timestamp_url` and
`tsp` for a local certificate, and `sign_command` for handing the artefact to an external signer
(`tauri-utils-2.9.3/src/config.rs:1042-1080`).

Sizing: two to three days, most of it procurement and CI plumbing rather than code. SmartScreen
reputation is a separate slow problem. An OV certificate starts with no reputation and downloads
warn until enough installs accumulate. EV skips that. If first-run warnings are unacceptable at
launch, buy EV.

### Linux

No OS-level signing gate. An unsigned AppImage or `.deb` runs. Signing is repository hygiene:
`.deb` packages signed for an apt repo, AppImages GPG-signed with a detached signature, Flatpak
repos signed by the remote. Sizing: half a day, and it can wait until there is a repo to sign for.

## Verdict per OS

| | macOS | Windows | Linux |
|---|---|---|---|
| Window plus Rust IPC command | Works, OBSERVED | EXPECTED, WebView2, untested | EXPECTED, builds CONTAINER, untested on a desktop |
| iroh in-process with the UI | Works under 60 GiB of concurrent transfer, OBSERVED | EXPECTED, platform-independent code | EXPECTED, platform-independent code |
| Runtime sharing | One tokio runtime we own, Tauri borrows it, OBSERVED | Same code path, EXPECTED | Same code path, EXPECTED |
| Bulk data path | localhost streamer, 5 GiB at 2.1 GB/s, RSS peak 87 MiB, OBSERVED | EXPECTED, mixed-content check needed if `useHttpsScheme` is on | EXPECTED, mixed-content check needed for the `tauri://` origin |
| User-level service | launchd agent, survives window close and `kill -9`, OBSERVED | Scheduled Task with a logon trigger, READ. SCM service rejected: session 0 cannot reach the user | systemd user unit plus `enable-linger`, READ |
| Signing | 1 day plus enrolment, tooling already present, OBSERVED | 2 to 3 days, hardware or cloud HSM mandatory since June 2023, READ | Half a day, no gate, READ |

Overall: the approach in the issue holds. Tauri hosting `rfm-core` in-process is sound, the two
want one runtime and share it cleanly, and the service-plus-window split works on the one OS that
could be tested. Nothing found here argues for a different shell.

## What it means for the plan

Issue #2 (keystore) is promoted to a blocker. A service that regenerates its identity on every
restart cannot be paired with, and the launchd `kill -9` test showed exactly that happening.
Anything that pairs devices waits on #2.

The desktop agent becomes two processes rather than one: a service that holds the endpoint and the
device identity, and a window that is a client of it. `agent/` staying a plain binary is right for
now, and the Tauri shell should be introduced as a separate crate that talks to it, rather than by
growing a window onto the agent.

A new issue is needed for the window-to-service IPC, most likely a Unix domain socket on macOS and
Linux and a named pipe on Windows, kept entirely in Rust so the UI bundle never opens a socket
itself (r3 §7).

Bulk file transfer to the webview should be specified as a token-gated loopback HTTP server, with
the uri-scheme handler reserved for small ranged reads. That is a design decision the file
protocol issues can now assume.

Two half-day verification tasks should be scheduled once machines exist: a Windows 11 VM to test
the Scheduled Task and the WebView2 mixed-content question, and a Linux desktop VM to test the
systemd user unit and the WebKitGTK origin question. Neither is a research task any more, only a
confirmation.

Signing procurement should start before it is needed, because both Apple enrolment and a hardware
or cloud code signing certificate have lead times measured in weeks.

## Reproducing

```sh
cd spikes/desktop-shell
cargo build --release

# window with a live endpoint id, a directory listing and both transfer paths
mkfile 5g /tmp/big.bin
BIG=/tmp/big.bin ./bench.sh list,http,proto

# the counterfactual: uncapped single response, watch RSS track the file size
mkfile 1g /tmp/one.bin
SPIKE_NO_CAP=1 BIG=/tmp/one.bin ./bench.sh protoraw

# service model
./launchd/install.sh
./target/release/probe          # with no window running
./launchd/uninstall.sh
```
