//! The device key: one Ed25519 key pair, generated on first run and never regenerated.
//!
//! r3 §3. The public key is the device ID, the trust-list entry key, and iroh's endpoint
//! id, so a new key is a new device. A run that cannot read the stored key must fail
//! rather than mint a fresh one, which would silently drop the machine out of every
//! workspace it belongs to. That rule is why [`DeviceKeyStore::load`] distinguishes an
//! empty store from an unreachable one.
//!
//! Where the key lives is [`store`]'s business, decided by spike #2
//! (`docs/spikes/keystore.md`). iroh cannot take an external signer, so the 32-byte seed
//! is in process memory whenever the agent runs and storage only protects it at rest.

mod store;

use std::path::PathBuf;

use iroh::{PublicKey, SecretKey};

#[cfg(windows)]
pub use self::store::DpapiFile;
#[cfg(any(target_os = "macos", target_os = "linux"))]
pub use self::store::Keystore;
pub use self::store::{SeedFile, default_store};

/// An Ed25519 seed is 32 bytes.
pub const SEED_LEN: usize = 32;

/// The private half of a device key, as stored.
pub type Seed = [u8; SEED_LEN];

/// Somewhere a device seed can be kept between runs.
///
/// One method pair over 32 bytes. Implementations are per platform and live in [`store`];
/// [`default_store`] picks the right one.
pub trait DeviceKeyStore {
    /// Reads the seed.
    ///
    /// `Ok(None)` means the store is reachable and holds nothing, which is first run.
    /// [`KeyStoreError::Unavailable`] means the store could not be read at all, which is a
    /// locked keychain or a keyring daemon that is not up yet. Callers wait and retry on
    /// the second and generate only on the first.
    fn load(&self) -> Result<Option<Seed>, KeyStoreError>;

    /// Writes the seed, replacing whatever was there.
    fn store(&self, seed: &Seed) -> Result<(), KeyStoreError>;

    /// A short name for logs and errors, such as `macOS keychain`.
    fn describe(&self) -> String;
}

/// Everything that can go wrong reaching the device key.
#[derive(Debug, thiserror::Error)]
pub enum KeyStoreError {
    /// The store could not be read. Never treat this as "no key".
    #[error("device key store unavailable: {0}")]
    Unavailable(String),

    /// The store held something, and it was not a 32-byte seed.
    #[error("stored device key is {0} bytes, expected {SEED_LEN}")]
    WrongLength(usize),

    /// A seed file other users can read. r3 §20.3.
    #[error("device key file {} is mode {mode:04o}, expected 0600", path.display())]
    TooOpen {
        /// The offending file.
        path: PathBuf,
        /// Its permission bits.
        mode: u32,
    },

    /// The store accepted a write and then returned something else.
    #[error("device key store {0} accepted the key and did not keep it")]
    NotPersisted(String),

    /// A migration found a different key already in the destination.
    #[error("device key file and key store hold different keys; refusing to choose between them")]
    ConflictingKeys,

    /// The OS CSPRNG failed.
    #[error("could not generate a device key")]
    Random(#[from] getrandom::Error),

    /// Anything the filesystem reported.
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

/// This device's key pair.
///
/// Holds the secret in memory for the life of the agent, because iroh's endpoint needs it
/// on every handshake (spike #2).
#[derive(Clone)]
pub struct DeviceIdentity {
    secret: SecretKey,
}

impl DeviceIdentity {
    /// Loads the device key, generating one only if the store is reachable and empty.
    pub fn load_or_generate(store: &dyn DeviceKeyStore) -> Result<Self, KeyStoreError> {
        if let Some(seed) = store.load()? {
            return Ok(Self::from_seed(seed));
        }

        let mut seed = [0u8; SEED_LEN];
        getrandom::fill(&mut seed)?;
        store.store(&seed)?;

        // Read it back before the key is used for anything. A store that accepts writes
        // and keeps nothing has to fail here, on first run, rather than at the next
        // restart where the regenerated key looks like a brand new device.
        match store.load()? {
            Some(written) if written == seed => Ok(Self::from_seed(seed)),
            _ => Err(KeyStoreError::NotPersisted(store.describe())),
        }
    }

    /// Rebuilds an identity from a seed already in hand.
    pub fn from_seed(seed: Seed) -> Self {
        Self {
            secret: SecretKey::from_bytes(&seed),
        }
    }

    /// The device ID.
    pub fn public_key(&self) -> PublicKey {
        self.secret.public()
    }

    /// The signing key, for the iroh endpoint (#5) and for trust-list signatures (#7).
    pub fn secret_key(&self) -> &SecretKey {
        &self.secret
    }

    /// What the pairing screens show (r3 §8.2 step 2, §8.3 step 3).
    pub fn fingerprint(&self) -> String {
        fingerprint(&self.public_key())
    }
}

/// Deliberately prints the public half only, so the seed cannot reach a log by accident.
impl std::fmt::Debug for DeviceIdentity {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DeviceIdentity")
            .field("public_key", &self.public_key())
            .finish_non_exhaustive()
    }
}

/// Renders a device key for a human to compare across two screens.
///
/// The whole key, z-base-32, in groups of four. z-base-32 is Zimmermann's alphabet for
/// exactly this job: it drops the characters people confuse when reading aloud or
/// retyping. Nothing is truncated, because a fingerprint short enough to be convenient is
/// short enough to grind a collision for, and the only defence the pairing flow has
/// against a substituted key is the user's eyes (r3 §8.2).
///
/// One rendering, used by every screen that shows a key. Two renderings would mean a user
/// comparing a hex string on one machine against base32 on the other.
pub fn fingerprint(key: &PublicKey) -> String {
    let encoded = key.to_z32();
    let mut out = String::with_capacity(encoded.len() + encoded.len() / 4);
    for (i, c) in encoded.chars().enumerate() {
        if i > 0 && i % 4 == 0 {
            out.push(' ');
        }
        out.push(c);
    }
    out
}

/// Moves a plain seed file into `store` and deletes the file.
///
/// Returns whether anything moved, so calling it on every start is safe: once the file is
/// gone it does nothing. An interrupted run that wrote the key but not yet deleted the
/// file finishes the job on the next call.
pub fn migrate_seed_file(
    file: &SeedFile,
    store: &dyn DeviceKeyStore,
) -> Result<bool, KeyStoreError> {
    let Some(seed) = file.load()? else {
        return Ok(false);
    };

    match store.load()? {
        // The write landed last time and the delete did not.
        Some(existing) if existing == seed => {
            file.remove()?;
            Ok(true)
        }
        // Two different identities. Picking one changes the device ID, so pick neither.
        Some(_) => Err(KeyStoreError::ConflictingKeys),
        None => {
            store.store(&seed)?;
            if store.load()? != Some(seed) {
                return Err(KeyStoreError::NotPersisted(store.describe()));
            }
            file.remove()?;
            Ok(true)
        }
    }
}

#[cfg(test)]
mod tests {
    use std::cell::RefCell;

    use super::*;
    use crate::config::ConfigDir;

    /// A store with the failure modes real ones have and no platform behind it.
    struct FakeStore {
        held: RefCell<Option<Seed>>,
        /// Reads fail while writes still succeed, which is what a locked keychain looks
        /// like from here. Failing both would let a caller that tried to generate anyway
        /// pass the test on the write error instead of on the read one.
        unreadable: bool,
        forgetful: bool,
    }

    impl FakeStore {
        fn working() -> Self {
            Self {
                held: RefCell::new(None),
                unreadable: false,
                forgetful: false,
            }
        }
    }

    impl DeviceKeyStore for FakeStore {
        fn load(&self) -> Result<Option<Seed>, KeyStoreError> {
            if self.unreadable {
                return Err(KeyStoreError::Unavailable("locked".into()));
            }
            Ok(*self.held.borrow())
        }

        fn store(&self, seed: &Seed) -> Result<(), KeyStoreError> {
            if !self.forgetful {
                *self.held.borrow_mut() = Some(*seed);
            }
            Ok(())
        }

        fn describe(&self) -> String {
            "fake store".into()
        }
    }

    #[test]
    fn the_second_run_gets_the_same_device_id() {
        let store = FakeStore::working();
        let first = DeviceIdentity::load_or_generate(&store).unwrap();
        let second = DeviceIdentity::load_or_generate(&store).unwrap();
        assert_eq!(first.public_key(), second.public_key());
    }

    #[test]
    fn an_unavailable_store_never_generates() {
        // The whole point of the Unavailable variant. A locked keychain that produced a
        // fresh key here would drop the machine out of its workspace with no way back.
        let store = FakeStore {
            unreadable: true,
            ..FakeStore::working()
        };
        let err = DeviceIdentity::load_or_generate(&store).unwrap_err();
        assert!(matches!(err, KeyStoreError::Unavailable(_)));
        assert!(
            store.held.borrow().is_none(),
            "a key was generated and written over an unreadable store"
        );
    }

    #[test]
    fn a_store_that_keeps_nothing_fails_on_the_first_run() {
        let store = FakeStore {
            forgetful: true,
            ..FakeStore::working()
        };
        let err = DeviceIdentity::load_or_generate(&store).unwrap_err();
        assert!(matches!(err, KeyStoreError::NotPersisted(_)));
    }

    #[test]
    fn migrating_a_plain_file_is_idempotent() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let file = SeedFile::in_config(&dir);
        let store = FakeStore::working();

        let original = DeviceIdentity::load_or_generate(&file)
            .unwrap()
            .public_key();

        assert!(migrate_seed_file(&file, &store).unwrap());
        assert!(!file.path().exists());
        assert_eq!(
            DeviceIdentity::load_or_generate(&store)
                .unwrap()
                .public_key(),
            original
        );

        // Every later start calls this and finds nothing left to do.
        assert!(!migrate_seed_file(&file, &store).unwrap());
        assert_eq!(
            DeviceIdentity::load_or_generate(&store)
                .unwrap()
                .public_key(),
            original
        );
    }

    #[test]
    fn a_migration_onto_a_different_key_is_refused() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let file = SeedFile::in_config(&dir);
        let store = FakeStore::working();

        DeviceIdentity::load_or_generate(&file).unwrap();
        DeviceIdentity::load_or_generate(&store).unwrap();

        let err = migrate_seed_file(&file, &store).unwrap_err();
        assert!(matches!(err, KeyStoreError::ConflictingKeys));
        assert!(file.path().exists());
    }

    #[test]
    fn a_half_finished_migration_completes() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let file = SeedFile::in_config(&dir);
        let store = FakeStore::working();

        let seed = [9u8; SEED_LEN];
        file.store(&seed).unwrap();
        store.store(&seed).unwrap();

        assert!(migrate_seed_file(&file, &store).unwrap());
        assert!(!file.path().exists());
    }

    #[test]
    fn the_fingerprint_carries_the_whole_key() {
        let key = DeviceIdentity::from_seed([1u8; SEED_LEN]).public_key();
        let shown = fingerprint(&key);

        // Groups of four, and every character of the encoding survives the grouping.
        let joined: String = shown.split(' ').collect();
        assert_eq!(joined, key.to_z32());
        assert_eq!(joined.len(), 52, "32 bytes in base32");
        assert!(shown.split(' ').all(|g| g.len() <= 4 && !g.is_empty()));
        assert_eq!(shown.matches(' ').count(), 12);
    }

    #[test]
    fn a_different_key_gets_a_different_fingerprint() {
        let a = DeviceIdentity::from_seed([1u8; SEED_LEN]);
        let b = DeviceIdentity::from_seed([2u8; SEED_LEN]);
        assert_ne!(a.fingerprint(), b.fingerprint());
    }

    #[test]
    fn debug_does_not_print_the_seed() {
        let identity = DeviceIdentity::from_seed([7u8; SEED_LEN]);
        let shown = format!("{identity:?}");
        assert!(!shown.contains("07070707"));
        assert!(shown.contains(&identity.public_key().to_string()));
    }
}
