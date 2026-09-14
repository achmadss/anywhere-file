//! Persistence: one file per workspace, replaced atomically.

use std::{io, path::PathBuf};

use super::{DecodeError, SignedTrustList, WorkspaceId};
use crate::config::{ConfigDir, write_atomic};

/// The trust lists this device holds, keyed by workspace.
///
/// A device is in several workspaces at once (r3 §11.1), so this is a directory of files
/// rather than one file. The file name is the workspace id in hex, which makes it the same
/// string that appears in the `ws=` TXT record (#9) and in the cloud's tables (#19).
#[derive(Debug, Clone)]
pub struct TrustStore {
    dir: PathBuf,
}

impl TrustStore {
    pub fn new(config: &ConfigDir) -> Self {
        Self {
            dir: config.trust_dir(),
        }
    }

    /// Reads and decodes the list held for `workspace`. Does not verify the signature:
    /// [`SignedTrustList::verify`] is the caller's to run, and #8 has more to check after
    /// it.
    pub fn load(&self, workspace: WorkspaceId) -> Result<SignedTrustList, StoreError> {
        let bytes = std::fs::read(self.path(workspace))?;
        Ok(SignedTrustList::decode(&bytes)?)
    }

    /// Replaces the list held for its workspace, atomically (r3 §8.1).
    ///
    /// Refuses a list whose signature does not verify. A store that will hold bytes nobody
    /// checked is a store that hands unverified bytes to whatever reads next, and the read
    /// path is where an omission is hardest to notice. Verifying is not the whole of §8.1's
    /// acceptance rule, which is #8's, but it is the part that can be enforced here.
    pub fn save(&self, list: &SignedTrustList) -> Result<(), StoreError> {
        list.verify()?;
        write_atomic(&self.path(list.list().workspace_id), &list.encode())?;
        Ok(())
    }

    /// Whether this device holds a list for `workspace`.
    pub fn contains(&self, workspace: WorkspaceId) -> bool {
        self.path(workspace).exists()
    }

    fn path(&self, workspace: WorkspaceId) -> PathBuf {
        self.dir.join(format!("{workspace}.tl"))
    }
}

/// Why a trust list could not be read or written.
#[derive(Debug, thiserror::Error)]
pub enum StoreError {
    #[error("reading or writing the trust list failed")]
    Io(#[from] io::Error),
    #[error("the stored bytes are not a trust list")]
    Decode(#[from] DecodeError),
    #[error("refusing to store a trust list that does not verify")]
    Verify(#[from] super::VerifyError),
}

#[cfg(test)]
mod tests {
    use iroh::SecretKey;

    use super::*;
    use crate::trust::{Entry, Role, Status, TrustList};

    fn list(workspace: WorkspaceId, admin: &SecretKey) -> TrustList {
        TrustList {
            workspace_id: workspace,
            name: "kitchen table".to_owned(),
            version: 1,
            entries: vec![Entry {
                device_key: admin.public(),
                display_name: "laptop".to_owned(),
                role: Role::Admin,
                account_id: None,
                status: Status::Active,
            }],
        }
    }

    /// #7's acceptance: sign, persist, reload, verify.
    #[test]
    fn round_trips_through_the_disk() {
        let tmp = tempfile::tempdir().unwrap();
        let store = TrustStore::new(&ConfigDir::open(tmp.path()).unwrap());
        let admin = SecretKey::generate();
        let workspace = WorkspaceId::generate();

        assert!(!store.contains(workspace));
        let signed = list(workspace, &admin).sign(&admin);
        store.save(&signed).unwrap();

        assert!(store.contains(workspace));
        let loaded = store.load(workspace).unwrap();
        loaded.verify().unwrap();
        assert_eq!(loaded, signed);
    }

    /// A device is in several workspaces at once (r3 §11.1), so one must not overwrite
    /// another.
    #[test]
    fn workspaces_do_not_collide() {
        let tmp = tempfile::tempdir().unwrap();
        let store = TrustStore::new(&ConfigDir::open(tmp.path()).unwrap());
        let admin = SecretKey::generate();
        let (a, b) = (WorkspaceId::generate(), WorkspaceId::generate());

        store.save(&list(a, &admin).sign(&admin)).unwrap();
        let mut second = list(b, &admin);
        second.name = "workshop".to_owned();
        store.save(&second.sign(&admin)).unwrap();

        assert_eq!(store.load(a).unwrap().list().name, "kitchen table");
        assert_eq!(store.load(b).unwrap().list().name, "workshop");
    }

    #[test]
    fn a_later_version_replaces_the_file_whole() {
        let tmp = tempfile::tempdir().unwrap();
        let store = TrustStore::new(&ConfigDir::open(tmp.path()).unwrap());
        let admin = SecretKey::generate();
        let workspace = WorkspaceId::generate();

        store.save(&list(workspace, &admin).sign(&admin)).unwrap();
        let mut v2 = list(workspace, &admin);
        v2.version = 2;
        v2.entries.push(Entry {
            device_key: SecretKey::generate().public(),
            display_name: "phone".to_owned(),
            role: Role::Standard,
            account_id: Some("acct_02".to_owned()),
            status: Status::Active,
        });
        store.save(&v2.sign(&admin)).unwrap();

        let loaded = store.load(workspace).unwrap();
        assert_eq!(loaded.list().version, 2);
        assert_eq!(loaded.list().entries.len(), 2);
        loaded.verify().unwrap();
    }

    #[test]
    fn a_list_that_does_not_verify_is_never_written() {
        let tmp = tempfile::tempdir().unwrap();
        let store = TrustStore::new(&ConfigDir::open(tmp.path()).unwrap());
        let admin = SecretKey::generate();
        let workspace = WorkspaceId::generate();

        let signed = list(workspace, &admin).sign(&admin);
        let tampered = SignedTrustList::from_parts(
            {
                let mut l = signed.list().clone();
                l.version = 99;
                l
            },
            *signed.signer(),
            *signed.signature(),
        );

        assert!(matches!(store.save(&tampered), Err(StoreError::Verify(_))));
        assert!(!store.contains(workspace));
    }

    #[test]
    fn a_corrupt_file_is_an_error_and_not_a_partial_list() {
        let tmp = tempfile::tempdir().unwrap();
        let config = ConfigDir::open(tmp.path()).unwrap();
        let store = TrustStore::new(&config);
        let workspace = WorkspaceId::generate();

        std::fs::write(
            config.trust_dir().join(format!("{workspace}.tl")),
            b"not a trust list",
        )
        .unwrap();
        assert!(matches!(store.load(workspace), Err(StoreError::Decode(_))));
    }
}
