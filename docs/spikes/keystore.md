# Spike #2: OS keystore for the Ed25519 device key

> The Rust code this report describes was removed on 2026-09-17 with the change of design.
> The per-OS findings stand and the Go agent is held to them. See `README.md` in this directory.

Issue: [#2](https://github.com/achmadss/anywhere-file/issues/2). The throwaway crate that
produced the output below lived in `spikes/keystore/` and was deleted once the design landed
in `device/core/src/identity/` (#6). `git log -- spikes/keystore` has it.

## Evidence levels

Every per-OS claim below carries one of these. Only one desktop OS was available.

| Level | Means |
| --- | --- |
| OBSERVED | Run on this machine, macOS 15 (Darwin 24.6.0) arm64, output pasted. |
| CONTAINER | Run in a Linux container under OrbStack. Shares the host kernel, has no desktop session and no D-Bus session bus, so it says something about libraries, syscalls and protocol behaviour and nothing about a real desktop. |
| READ | From crate source on disk or vendor documentation, cited by `file:line` or URL. |
| EXPECTED | Reasoning from the above. Could be wrong. |

Nothing here was run on Windows or on a Linux desktop. There is no such machine in this
environment.

## The question that decides everything

Can iroh accept an external signer instead of raw key bytes?

No. Not in v1.2.0, and there is no deprecated-but-present path either.

`SecretKey` is a newtype over `ed25519_dalek::SigningKey`, with no trait between them
(`iroh-base/src/key.rs:15` for the import, `iroh-base/src/key.rs:261`):

```rust
pub struct SecretKey(SigningKey);
```

Every constructor takes bytes: `from_bytes` (`key.rs:334`), `From<[u8; 32]>` (`key.rs:349`),
`TryFrom<&[u8]>` (`key.rs:361`), `FromStr` over base32 or hex (`key.rs:269`), `Deserialize`
(`key.rs:287`). The type also goes the other way, `to_bytes` at `key.rs:330` and `Serialize`
at `key.rs:278`, so it is built around material that is present and exportable in process.

The only door into an endpoint takes that concrete type (`iroh/src/endpoint.rs:531`):

```rust
pub fn secret_key(mut self, secret_key: SecretKey) -> Self {
```

The builder field is `Option<SecretKey>` (`iroh/src/endpoint.rs:132`) and an unset key is
generated at bind (`iroh/src/endpoint.rs:228`). Three places sign with it, and all three take
`&SecretKey`:

- QUIC/TLS raw-public-key handshake, `iroh/src/tls/resolver.rs:92`
- relay authentication, `iroh-relay/src/protos/handshake.rs:228` and `:266`
- pkarr DNS record publishing, `iroh-dns/src/pkarr.rs:79`

The interesting part is that the seam already exists internally. iroh routes all TLS signing
through rustls' own signer trait (`iroh/src/tls/resolver.rs:69` and `:92`):

```rust
impl rustls::sign::SigningKey for IrohSecretKey { ... }
impl rustls::sign::Signer for IrohSecretKey {
    fn sign(&self, message: &[u8]) -> Result<Vec<u8>, rustls::Error> {
        Ok(self.key.sign(message).to_bytes().to_vec())
    }
```

That is exactly the shape an external signer needs. It is unreachable from outside the crate:
`resolver` is a private module (`iroh/src/tls.rs:19`) and `TlsConfig` is `pub(crate)`
(`iroh/src/tls.rs:45`). Even reaching it would only cover TLS, leaving relay auth and pkarr
still holding raw bytes.

Upstream, [n0-computer/iroh#2355](https://github.com/n0-computer/iroh/issues/2355) "Allow
using iroh with other signers" has been open since 2024-06-09, asking for FIDO tokens, HSMs
and PKCS#11 smartcards. The last activity is a comment on 2026-08-06 suggesting
`signature::Signer<ed25519::Signature>` as the trait. A maintainer's 2024-10-10 comment says
"We are probably going to keep PrivateKey put pass the discovery traits a 'signer' instead of
always taking a private key", which is about discovery only and would not cover the TLS or
relay paths. No PR is attached. Treat it as not coming.

So a genuinely non-exportable device key is off the table for as long as we are on iroh's
`SecretKey`. Everything below is about protecting 32 bytes that will sit in process memory
regardless.

## What the keystore actually buys us

It protects the key at rest, when the machine is off, when the user is logged out, and against
another user account on the same box. It does not protect against a process running as the
same user while the session is unlocked. The macOS section below measures that.

## macOS: works, OBSERVED

Crate: `keyring` 3.6.3 with the `apple-native` feature. It calls
`security_framework::os::macos::keychain::SecKeychain` and
`security_framework::os::macos::passwords::find_generic_password`
(`keyring-3.6.3/src/macos.rs:31-33`), which is the legacy file-based keychain API, so the item
lands in the login keychain as a generic password.

The throwaway binary has `store`, `load` and `wipe` subcommands, run as separate processes so
"restart" is a real restart. It writes the key twice, once as the raw seed in the keystore and
once as a wrapping key in the keystore plus an XChaCha20-Poly1305 sealed file, then reloads
both and runs an iroh handshake with the reloaded key.

```
$ ./target/release/spike-keystore store
generated       public key = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b
direct          wrote 32 bytes to keystore dev.anywherefile.spike-keystore/device-key-seed
wrapped         wrapping key in keystore dev.anywherefile.spike-keystore/device-key-wrapping-key, 72 byte sealed file at /var/folders/rm/.../T/spike-keystore-device.key.sealed

expected public key = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b

$ ./target/release/spike-keystore load          # separate process
direct          reloaded public key = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b
wrapped         reloaded public key = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b
both shapes agree on the seed

handshake       peer saw remote_id = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b
handshake       payload = "hello from the keystore"
handshake       OK: matches the reloaded key
```

The handshake is two endpoints in one process with `RelayMode::Disabled`, dialling over
loopback. The accepting side reads `Connection::remote_id()`, which iroh only produces after
the raw-public-key TLS handshake verified a signature from the dialling key. Bytes that had
merely round-tripped through storage could not produce that, so a match proves the reloaded
bytes are the live signing key.

Both checks were broken deliberately to confirm they run. One byte flipped in the sealed file:

```
$ printf '\xff' | dd of=...spike-keystore-device.key.sealed bs=1 seek=40 conv=notrunc
$ ./target/release/spike-keystore load
direct          reloaded public key = 328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b
Error: "aead::Error"
exit=1
```

And the client endpoint rebuilt with a freshly generated key while still expecting the stored
one:

```
handshake       peer saw remote_id = dcaf17b96fb42d1d6d035f5ea9964d344c017234055864dcb20fb0b7c016a0fb
thread 'main' panicked at src/main.rs:138:5:   # line number as of the committed, formatted source
assertion `left == right` failed: peer authenticated a different key
  left: PublicKey(dcaf17b96fb42d1d6d035f5ea9964d344c017234055864dcb20fb0b7c016a0fb)
 right: PublicKey(328051368d62c8b4e27bd916f5e4128a0665a4738379eaae2257943e538b623b)
```

### Where the item lands

```
$ security find-generic-password -s dev.anywherefile.spike-keystore -a device-key-seed
keychain: "/Users/achmad/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    "acct"<blob>="device-key-seed"
    "svce"<blob>="dev.anywherefile.spike-keystore"
```

Login keychain, generic password class. It is protected by the login keychain's lock state and
nothing more.

### Locked keychain

Tested on a throwaway keychain rather than the login keychain, to avoid a GUI password prompt
in a non-interactive session:

```
$ security create-keychain -p testpass /tmp/spikelock.keychain-db
$ security add-generic-password -s spike.lock.test -a acct -w secretvalue /tmp/spikelock.keychain-db
$ security find-generic-password -s spike.lock.test -a acct -w /tmp/spikelock.keychain-db
secretvalue
$ security lock-keychain /tmp/spikelock.keychain-db
$ security find-generic-password -s spike.lock.test -a acct -w /tmp/spikelock.keychain-db
exit=128
$ security error -128
Error: 0xFFFFFF80 -128 User canceled the operation.
```

A locked keychain fails the read with `errSecUserCanceled`. In a GUI session it prompts for
the keychain password first and only fails if the user dismisses it. The agent must treat "key
unavailable" as a normal transient state, hold off starting the endpoint, and retry, rather
than treating it as "no key, generate a new identity". Generating a new identity on a locked
keychain would silently change the device's identity, which is the worst failure available
here.

### The ACL is not app-bound

```
$ cp target/release/spike-keystore /tmp/different-binary-name
$ /tmp/different-binary-name load
direct          reloaded public key = 3280513...   # no prompt

$ security find-generic-password -s dev.anywherefile.spike-keystore -a device-key-seed -w
f1f0c5685562bb268901329b9eb2a349e93f62e9734d4848bb928471ce0db1a8
```

`/usr/bin/security` is an unrelated Apple-signed binary and it printed the raw seed with no
prompt. Any process running as the logged-in user can read the device key while the keychain
is unlocked. The spike binaries here are ad-hoc linker-signed, so a properly signed and
notarized app might get a tighter default ACL, but that is EXPECTED and should be rechecked
once there is a signed build.

### Secure Enclave does not help

The Secure Enclave is the only way to get a non-exportable key on macOS, and it cannot hold an
Ed25519 key. `security-framework-3.7.0/src/key.rs:329-336`:

```rust
pub enum Token {
    /// Generate the key in software, compatible with all `KeyType`s.
    Software,
    /// Generate the key in the Secure Enclave such that the private key is not
    /// extractable. Only compatible with `KeyType::ec()`.
    SecureEnclave,
}
```

`KeyType` offers `rsa()`, `aes()`, `ec()` and `ec_sec_prime_random()`
(`key.rs:56, 70, 104, 112`) and no Ed25519 variant at all. Even if iroh took a signer, the
Secure Enclave could not be that signer for an Ed25519 identity.

## Windows: READ only, untested

Crate: `keyring` 3.6.3 with `windows-native`, which uses Windows Credential Manager generic
credentials through `CredWriteW` / `CredReadW` / `CredDeleteW`
(`keyring-3.6.3/src/windows.rs:50-54`). Credential Manager blobs are encrypted with DPAPI
under the user's logon credentials, so this is the DPAPI story r3 asked about, one layer up.
The blob limit is `CRED_MAX_CREDENTIAL_BLOB_SIZE = 2560` bytes
(`windows-sys-0.52.0/src/Windows/Win32/Security/Credentials/mod.rs:236`), comfortably above 32.

One problem, found by reading. `keyring` hardcodes the persistence scope
(`keyring-3.6.3/src/windows.rs:246` and again at `:555`):

```rust
let persist = CRED_PERSIST_ENTERPRISE;
```

`CRED_PERSIST_ENTERPRISE` is 3 and `CRED_PERSIST_LOCAL_MACHINE` is 2
(`windows-sys-0.52.0/.../Credentials/mod.rs:248-249`). Enterprise persistence means the
credential travels with a roaming user profile. A device key that follows the user to another
machine is not a device key: two machines would come up with the same iroh `EndpointId`, and
iroh has no concept of the same identity on two hosts. Nothing in `keyring`'s public API
overrides this.

So on Windows the plan is to skip `keyring` and use DPAPI directly, via the `windows-dpapi`
crate (0.2.0, [docs.rs/windows-dpapi](https://docs.rs/windows-dpapi)) which wraps
`CryptProtectData` / `CryptUnprotectData` with a `Scope` of `User` or `Machine`, or via
`windows-sys` `Win32::Security::Cryptography::CryptProtectData` directly. That produces a
ciphertext blob we own and write to our own config directory, which is the wrapped-file shape,
with DPAPI holding the wrapping key rather than a keystore item.

Untested: everything in this section. No Windows machine, and a Linux container cannot
substitute for one in any way. To actually test it: a Windows 10 or 11 VM or runner, `cargo run
-- store` then `cargo run -- load`, plus a check of `CredEnumerate`/`rundll32
keymgr.dll,KRShowKeyMgr` to confirm the persistence scope actually written, plus a
roaming-profile test on a domain-joined pair to confirm the roaming claim.

## Linux Secret Service: READ, and the container is useless here

The Secret Service is a D-Bus session-bus service provided by gnome-keyring or KWallet, tied to
a logged-in desktop session and typically unlocked by the login password via PAM. A container
shares the host kernel and has no desktop session, no display manager, no PAM login and no
session bus, so running the code in one tells you nothing about whether GNOME Keyring returns
the right bytes on a real Fedora desktop. It was still worth running once, because it pins down
the exact error the fallback branch has to catch.

The container program, whole:

```rust
fn main() {
    // keyring::set_default_credential_builder(keyring::keyutils::default_credential_builder());
    let e = keyring::Entry::new("dev.anywherefile.spike", "device-key-seed").unwrap();
    let arg = std::env::args().nth(1).unwrap_or_default();
    if arg == "set" { println!("set_secret -> {:?}", e.set_secret(&[7u8; 32])); }
    println!("get_secret -> {:?}", e.get_secret());
}
```

with `keyring = { version = "3", features = ["sync-secret-service", "vendored",
"crypto-rust", "linux-native"] }`, run as
`docker run --rm -v .:/w -w /w rust:1-slim cargo run`. The commented line selects the kernel
keyring instead of the Secret Service and is uncommented for the headless section below.

`sync-secret-service`, CONTAINER:

```
set_secret -> Err(PlatformFailure(Dbus(D-Bus error: Using X11 for dbus-daemon autolaunch was
  disabled at compile time, set your DBUS_SESSION_BUS_ADDRESS instead
  (org.freedesktop.DBus.Error.NotSupported))))
get_secret -> Err(PlatformFailure(Dbus(...same...)))
```

That is the same shape a headless server produces, because the cause is the same: no
`DBUS_SESSION_BUS_ADDRESS`.

Untested: whether a real GNOME Keyring or KWallet stores and returns 32 raw bytes correctly,
whether the collection is unlocked at daemon start under a systemd user unit, and what happens
on a locked screen. To actually test it: a Fedora or Ubuntu desktop VM with a real login
session, `dbus-run-session` is not enough because it gives a bus with no Secret Service on it.

## Linux headless with no Secret Service: a file, plus honesty about it

This is a NAS or a home server with no desktop and no keyring. Two candidates were checked.

The kernel keyring (`keyutils`, `keyring`'s `linux-native` feature) is not a candidate, and the
reason is decisive. CONTAINER, two separate `docker run` invocations, which give separate
kernel session keyrings on the shared host kernel and so stand in for a reboot:

```
$ docker run --rm --security-opt seccomp=unconfined ... spike-linux-keyring set
set_secret -> Ok(())
get_secret -> Ok([7, 7, 7, ... 7])

$ docker run --rm --security-opt seccomp=unconfined ... spike-linux-keyring
get_secret -> Err(NoEntry)
```

Within one session it persists across a process restart. Across sessions it is gone. A session
keyring does not survive a reboot, so it cannot hold a device identity. Separately, Docker's
default seccomp profile blocks the `keyctl` syscall entirely, which is why the run above needs
`--security-opt seccomp=unconfined`:

```
called `Result::unwrap()` on an `Err` value: PlatformFailure(PermissionDenied)
```

That matters directly, because the agent running inside a Docker container on a NAS is a
realistic deployment.

A TPM 2.0 can seal a wrapping key to the machine, reachable from Rust via `tss-esapi`. That is
the only genuine hardware option on this target. It is EXPECTED to work and completely
untested, and most consumer NAS hardware, Synology included, ships without a TPM, so it cannot
be the baseline.

That leaves the honest answer to "who holds the wrapping key when there is no keyring and no
user session": nobody does. There is no secret on such a machine that is not on the same disk
as the key file. An encrypted key file whose wrapping key sits in a second file beside it is
theatre, and shipping it would be worse than shipping neither, because it invites the reader to
believe the key is protected.

So the headless fallback is a plain 32-byte seed file, mode 0600, owned by the service user, in
a mode 0700 directory, refusing to start if the permissions are wider. This is exactly what
OpenSSH does for a passphrase-less `~/.ssh/id_ed25519` and what WireGuard does for
`/etc/wireguard/*.conf`, and the security property is stated plainly in the docs: on a headless
box the device key is protected by filesystem permissions and by full-disk encryption if the
operator set it up. A TPM path can be added later behind the same interface for the machines
that have one.

## Android Keystore for v1.1: does not foreclose, does not solve

Android Keystore's documented hardware-backed algorithms are RSA, ECDSA over NIST P-224 /
P-256 / P-384 / P-521, AES and HMAC-SHA256
([source.android.com keystore features](https://source.android.com/docs/security/features/keystore/features)).
Ed25519 does not appear. AOSP framework code has an `AndroidKeyStoreEdECPrivateKey` class, so
some Ed25519 handling exists, but it is not in the documented hardware-backed set and would
have to be measured on real hardware before being relied on. EXPECTED: a hardware-backed
non-exportable Ed25519 identity key is not available on Android today, which lines up with iroh
not being able to use one anyway.

What Android does give us, documented and hardware-backed, is an AES key. That is enough for
the wrapped-file shape: a hardware AES key in the Keystore, `setUserAuthenticationRequired` off
so the daemon can start unattended, wrapping a seed file in app-private storage. This is the
same shape as the Windows DPAPI path, so choosing it now for Windows keeps Android open.

## Chosen design

One `DeviceKeyStore` trait in `core/` with a single method pair, load and store, over 32 bytes.
Per-platform implementations behind it:

macOS uses `keyring` with `apple-native` and stores the 32-byte seed directly as a login
keychain generic password. Verified end to end above.

Windows uses `windows-dpapi` with `Scope::User` and writes the DPAPI blob to our own config
directory, skipping `keyring` because of the `CRED_PERSIST_ENTERPRISE` roaming problem.

Linux with a desktop session uses `keyring` with `sync-secret-service`, storing the seed
directly, and falls back to the headless path when the D-Bus error above comes back.

Linux headless writes a 0600 seed file in a 0700 directory and says so in the docs.

r3 §20.3 guessed at an encrypted key file whose wrapping key lives in the keystore. On macOS and
Linux desktop that is one artifact too many. Both shapes were built and both work, and the
wrapped shape has two failure modes instead of one, the sealed file and the keystore item, which
can get out of sync. It buys nothing when the keystore has no size or type limit that a 32-byte
seed runs into, which neither macOS nor the Secret Service does, and Windows Credential Manager
has 2560 bytes of headroom. So the seed goes in directly wherever a keystore exists, and the
wrapped shape is used only on Windows, where DPAPI is an encrypt-this-blob API and gives us no
storage of its own.

### First run

Generate 32 bytes, write them through the store, then read them back and compare before doing
anything else with the identity. A store that silently persisted nothing has to fail loudly at
first run and not at the next restart. `keyring` 3 makes this concrete: with no `*-native`
feature enabled it falls back to an in-process mock credential store that accepts every write
and forgets everything, so the feature flags in `device/core/Cargo.toml` are load-bearing.

### Start-up load

Read, and distinguish three outcomes rather than two. Key present, use it. Store reachable and
empty, this is first run, generate. Store unreachable or locked, which on macOS is
`errSecUserCanceled` and on Linux is the D-Bus error above, wait and retry, and never generate.
Generating on a locked keystore changes the device identity permanently and silently.

## What changes for other issues

Nothing needs an external signer any more, so any design that assumed one can drop it. The
device key is exportable by design and the agent should be written on that basis: rotation and
revocation matter more than storage hardening, because storage hardening tops out at "protected
while logged out".

## Reproducing

The spike crate is gone. `git show <commit>:spikes/keystore` recovers it, or run the shipped
code: `device/core/src/identity/store.rs` uses the same `keyring` service name and account, so
`security find-generic-password -s dev.anywherefile.agent -a device-key-seed` shows the item
on macOS.
