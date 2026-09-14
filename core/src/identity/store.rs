//! Where the device seed lives, per platform. Spike #2 chose these; the reasoning, the
//! measurements and the untested parts are in `docs/spikes/keystore.md`.
//!
//! macOS puts the seed in the login keychain. Linux with a desktop session puts it in the
//! Secret Service. Windows wraps it with DPAPI under the user scope and writes the blob
//! next to the rest of the agent's state, because `keyring`'s Credential Manager backend
//! hardcodes `CRED_PERSIST_ENTERPRISE`, which roams the credential to other machines and
//! would give two hosts the same device ID. Anything else, a NAS or a headless server,
//! gets a mode 0600 file and a note in the docs saying so.

use std::{
    fs, io,
    path::{Path, PathBuf},
};

use super::{DeviceKeyStore, KeyStoreError, SEED_LEN, Seed};
use crate::config::{ConfigDir, write_atomic};

/// A 32-byte seed on disk with nothing wrapped around it.
///
/// The last resort, and the whole story on a machine with no keyring and no user session:
/// there is no secret on such a box that is not on the same disk as this file, so the key
/// is protected by file permissions and by whatever full-disk encryption the operator set
/// up. Sealing it with a wrapping key stored beside it would only look like more.
#[derive(Debug, Clone)]
pub struct SeedFile {
    path: PathBuf,
}

impl SeedFile {
    /// The conventional location, `<config>/identity/identity.key`.
    pub fn in_config(dir: &ConfigDir) -> Self {
        Self {
            path: dir.identity_dir().join("identity.key"),
        }
    }

    /// The file this store reads and writes.
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Deletes the file. Succeeds if it was already gone, so migration can be re-run.
    pub fn remove(&self) -> io::Result<()> {
        match fs::remove_file(&self.path) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            other => other,
        }
    }
}

impl DeviceKeyStore for SeedFile {
    fn load(&self) -> Result<Option<Seed>, KeyStoreError> {
        match fs::metadata(&self.path) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(e.into()),
            Ok(meta) => refuse_if_others_can_read(&self.path, &meta)?,
        }

        let bytes = fs::read(&self.path)?;
        let seed: Seed = bytes
            .as_slice()
            .try_into()
            .map_err(|_| KeyStoreError::WrongLength(bytes.len()))?;
        Ok(Some(seed))
    }

    fn store(&self, seed: &Seed) -> Result<(), KeyStoreError> {
        if let Some(dir) = self.path.parent() {
            fs::create_dir_all(dir)?;
            set_mode(dir, 0o700)?;
        }
        write_atomic(&self.path, seed)?;
        set_mode(&self.path, 0o600)?;
        Ok(())
    }

    fn describe(&self) -> String {
        format!("seed file {}", self.path.display())
    }
}

/// Refuses a key file any other user can read (r3 §20.3).
///
/// A no-op on Windows, whose mode bits are synthetic and always report 0666 or 0444
/// regardless of the real ACL, so a check there would fire on every correctly protected
/// file. DPAPI covers Windows instead.
#[cfg(unix)]
fn refuse_if_others_can_read(path: &Path, meta: &fs::Metadata) -> Result<(), KeyStoreError> {
    use std::os::unix::fs::PermissionsExt;

    let mode = meta.permissions().mode() & 0o777;
    if mode & 0o077 != 0 {
        return Err(KeyStoreError::TooOpen {
            path: path.to_path_buf(),
            mode,
        });
    }
    Ok(())
}

#[cfg(not(unix))]
fn refuse_if_others_can_read(_path: &Path, _meta: &fs::Metadata) -> Result<(), KeyStoreError> {
    Ok(())
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    use std::os::unix::fs::PermissionsExt;

    fs::set_permissions(path, fs::Permissions::from_mode(mode))
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) -> io::Result<()> {
    Ok(())
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
mod os_keystore {
    use super::{DeviceKeyStore, KeyStoreError, SEED_LEN, Seed};

    /// Namespaces the item. Changing it orphans every already-stored key.
    const SERVICE: &str = "dev.anywherefile.agent";
    const ACCOUNT: &str = "device-key-seed";

    #[cfg(target_os = "macos")]
    const WHERE: &str = "the login keychain";
    #[cfg(target_os = "linux")]
    const WHERE: &str = "the Secret Service";

    /// The seed as an OS credential.
    ///
    /// Protects the key while the machine is off, while the user is logged out, and from
    /// other accounts on the same box. It does not protect it from a process running as
    /// the same user on an unlocked session: spike #2 read the seed back with
    /// `/usr/bin/security` and got no prompt.
    pub struct Keystore {
        entry: Result<keyring::Entry, keyring::Error>,
    }

    impl Keystore {
        /// Addresses the credential. Any failure here surfaces on the first load.
        pub fn new() -> Self {
            Self {
                entry: keyring::Entry::new(SERVICE, ACCOUNT),
            }
        }

        fn entry(&self) -> Result<&keyring::Entry, KeyStoreError> {
            self.entry
                .as_ref()
                .map_err(|e| KeyStoreError::Unavailable(e.to_string()))
        }
    }

    impl Default for Keystore {
        fn default() -> Self {
            Self::new()
        }
    }

    impl std::fmt::Debug for Keystore {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(f, "Keystore({SERVICE}/{ACCOUNT})")
        }
    }

    impl DeviceKeyStore for Keystore {
        fn load(&self) -> Result<Option<Seed>, KeyStoreError> {
            match self.entry()?.get_secret() {
                Ok(bytes) => {
                    let seed: Seed = bytes
                        .as_slice()
                        .try_into()
                        .map_err(|_| KeyStoreError::WrongLength(bytes.len()))?;
                    Ok(Some(seed))
                }
                // Reachable and empty: first run.
                Err(keyring::Error::NoEntry) => Ok(None),
                // Locked, or the daemon is not up. Wait and retry, never generate.
                Err(e) => Err(KeyStoreError::Unavailable(e.to_string())),
            }
        }

        fn store(&self, seed: &Seed) -> Result<(), KeyStoreError> {
            debug_assert_eq!(seed.len(), SEED_LEN);
            self.entry()?
                .set_secret(seed)
                .map_err(|e| KeyStoreError::Unavailable(e.to_string()))
        }

        fn describe(&self) -> String {
            WHERE.to_string()
        }
    }
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
pub use os_keystore::Keystore;

/// A DPAPI-sealed seed in the agent's own config directory.
#[cfg(windows)]
#[derive(Debug, Clone)]
pub struct DpapiFile {
    path: PathBuf,
}

#[cfg(windows)]
impl DpapiFile {
    /// The conventional location, `<config>\identity\identity.dpapi`.
    pub fn in_config(dir: &ConfigDir) -> Self {
        Self {
            path: dir.identity_dir().join("identity.dpapi"),
        }
    }

    /// The file this store reads and writes.
    pub fn path(&self) -> &Path {
        &self.path
    }
}

#[cfg(windows)]
impl DeviceKeyStore for DpapiFile {
    fn load(&self) -> Result<Option<Seed>, KeyStoreError> {
        let sealed = match fs::read(&self.path) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
            other => other?,
        };
        // User scope: the blob is useless to another account on the same machine, and
        // useless on any other machine, which is what a device key should be.
        let bytes = windows_dpapi::decrypt_data(&sealed, windows_dpapi::Scope::User, None)
            .map_err(|e| KeyStoreError::Unavailable(e.to_string()))?;
        let seed: Seed = bytes
            .as_slice()
            .try_into()
            .map_err(|_| KeyStoreError::WrongLength(bytes.len()))?;
        Ok(Some(seed))
    }

    fn store(&self, seed: &Seed) -> Result<(), KeyStoreError> {
        if let Some(dir) = self.path.parent() {
            fs::create_dir_all(dir)?;
        }
        let sealed = windows_dpapi::encrypt_data(seed, windows_dpapi::Scope::User, None)
            .map_err(|e| KeyStoreError::Unavailable(e.to_string()))?;
        write_atomic(&self.path, &sealed)?;
        Ok(())
    }

    fn describe(&self) -> String {
        format!("DPAPI blob {}", self.path.display())
    }
}

/// Picks the store for this machine.
///
/// The caller passes the result to [`DeviceIdentity::load_or_generate`], and on a platform
/// with a keystore should first call [`migrate_seed_file`] so a plain file left by an
/// older build moves in.
///
/// [`DeviceIdentity::load_or_generate`]: super::DeviceIdentity::load_or_generate
/// [`migrate_seed_file`]: super::migrate_seed_file
#[cfg(target_os = "macos")]
pub fn default_store(_dir: &ConfigDir) -> Box<dyn DeviceKeyStore> {
    Box::new(Keystore::new())
}

/// Picks the store for this machine: the Secret Service under a desktop session, a plain
/// file otherwise.
///
/// The session bus address is the test rather than a failed Secret Service call, because a
/// call can also fail because the collection is locked, and a locked collection must not
/// fall back to a second store holding a different key.
#[cfg(target_os = "linux")]
pub fn default_store(dir: &ConfigDir) -> Box<dyn DeviceKeyStore> {
    if std::env::var_os("DBUS_SESSION_BUS_ADDRESS").is_some() {
        Box::new(Keystore::new())
    } else {
        Box::new(SeedFile::in_config(dir))
    }
}

/// Picks the store for this machine.
#[cfg(windows)]
pub fn default_store(dir: &ConfigDir) -> Box<dyn DeviceKeyStore> {
    Box::new(DpapiFile::in_config(dir))
}

/// Picks the store for this machine.
#[cfg(not(any(target_os = "macos", target_os = "linux", windows)))]
pub fn default_store(dir: &ConfigDir) -> Box<dyn DeviceKeyStore> {
    Box::new(SeedFile::in_config(dir))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::identity::DeviceIdentity;

    fn seed_file() -> (tempfile::TempDir, SeedFile) {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let file = SeedFile::in_config(&dir);
        (tmp, file)
    }

    #[test]
    fn an_empty_directory_is_first_run_and_not_an_error() {
        let (_tmp, file) = seed_file();
        assert_eq!(file.load().unwrap(), None);
    }

    #[test]
    fn a_seed_round_trips() {
        let (_tmp, file) = seed_file();
        let seed = [3u8; SEED_LEN];
        file.store(&seed).unwrap();
        assert_eq!(file.load().unwrap(), Some(seed));
    }

    #[test]
    fn a_truncated_file_is_an_error_and_not_a_new_identity() {
        let (_tmp, file) = seed_file();
        file.store(&[4u8; SEED_LEN]).unwrap();
        fs::write(file.path(), [4u8; 31]).unwrap();
        set_mode(file.path(), 0o600).unwrap();

        let err = file.load().unwrap_err();
        assert!(matches!(err, KeyStoreError::WrongLength(31)));
        // And the caller sees the error rather than a fresh key.
        assert!(DeviceIdentity::load_or_generate(&file).is_err());
    }

    #[cfg(unix)]
    #[test]
    fn the_file_is_written_owner_only() {
        use std::os::unix::fs::PermissionsExt;

        let (_tmp, file) = seed_file();
        file.store(&[5u8; SEED_LEN]).unwrap();

        let mode = fs::metadata(file.path()).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600, "seed file");
        let dir = fs::metadata(file.path().parent().unwrap())
            .unwrap()
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(dir, 0o700, "identity directory");
    }

    #[cfg(unix)]
    #[test]
    fn a_file_other_users_can_read_is_refused() {
        let (_tmp, file) = seed_file();
        file.store(&[6u8; SEED_LEN]).unwrap();
        set_mode(file.path(), 0o644).unwrap();

        let err = file.load().unwrap_err();
        assert!(matches!(err, KeyStoreError::TooOpen { mode: 0o644, .. }));
        assert!(DeviceIdentity::load_or_generate(&file).is_err());
    }

    /// The macOS and Secret Service round trips are not here: exercising them would write
    /// into the developer's own login keychain on every `cargo test`. Spike #2 ran the
    /// macOS one end to end, through an iroh handshake with the reloaded key. DPAPI is
    /// tested, because its store is an ordinary file in a temporary directory.
    #[cfg(windows)]
    #[test]
    fn a_dpapi_blob_round_trips_and_is_not_the_seed() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let store = DpapiFile::in_config(&dir);

        assert_eq!(store.load().unwrap(), None);

        let seed = [8u8; SEED_LEN];
        store.store(&seed).unwrap();
        assert_eq!(store.load().unwrap(), Some(seed));

        let on_disk = fs::read(store.path()).unwrap();
        assert!(
            !on_disk.windows(SEED_LEN).any(|w| w == seed),
            "the seed is sitting in the blob in the clear"
        );
    }

    #[test]
    fn remove_is_idempotent() {
        let (_tmp, file) = seed_file();
        file.store(&[7u8; SEED_LEN]).unwrap();
        file.remove().unwrap();
        file.remove().unwrap();
        assert_eq!(file.load().unwrap(), None);
    }
}
