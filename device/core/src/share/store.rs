//! Shares on disk, next to the trust lists.
//!
//! The file is JSON rather than the trust list's canonical encoding, because nothing signs
//! it. It is local configuration for one device, it never leaves the machine, and a person
//! debugging their own agent should be able to read it.

use std::{io, path::PathBuf};

use iroh::PublicKey;
use serde::{Deserialize, Serialize};

use super::{Permission, Share, ShareDefinition, ShareId};
use crate::{
    config::{ConfigDir, write_atomic},
    fs::ShareRoot,
    trust::{TrustList, WorkspaceId},
};

/// Bumped when the shape of the file changes in a way an older build cannot read.
const FORMAT_VERSION: u32 = 1;

#[derive(Serialize, Deserialize)]
struct ShareFile {
    version: u32,
    shares: Vec<Share>,
}

/// This device's shares.
///
/// Holds only shares this device owns. There is no constructor, no setter and no decoder
/// that produces a share naming another device, so "configure my neighbour's shares" is
/// unrepresentable rather than merely refused.
#[derive(Debug, Clone)]
pub struct ShareStore {
    path: PathBuf,
    device_key: PublicKey,
    shares: Vec<Share>,
}

impl ShareStore {
    /// Reads `shares.json`, or starts empty if there is none.
    pub fn open(dir: &ConfigDir, device_key: PublicKey) -> Result<Self, ShareError> {
        let path = dir.shares_file();
        let shares = match std::fs::read(&path) {
            Err(e) if e.kind() == io::ErrorKind::NotFound => Vec::new(),
            Err(e) => return Err(e.into()),
            Ok(bytes) => {
                let file: ShareFile = serde_json::from_slice(&bytes)?;
                if file.version != FORMAT_VERSION {
                    return Err(ShareError::UnknownFormat(file.version));
                }
                for share in &file.shares {
                    if share.device_key != device_key {
                        return Err(ShareError::ForeignShare(share.device_key));
                    }
                }
                file.shares
            }
        };
        Ok(Self {
            path,
            device_key,
            shares,
        })
    }

    /// Every share this device owns, in the order they were added.
    pub fn shares(&self) -> &[Share] {
        &self.shares
    }

    /// One share by id.
    pub fn get(&self, id: ShareId) -> Option<&Share> {
        self.shares.iter().find(|s| s.id == id)
    }

    /// Exposes a folder, and returns the new share's id.
    ///
    /// The root has to be absolute and has to open as a directory now, so a typo is caught
    /// here rather than on the first request from a peer. The share starts with no grants,
    /// so nothing is exposed until someone is added.
    pub fn add(
        &mut self,
        workspace_id: WorkspaceId,
        root: PathBuf,
        label: String,
    ) -> Result<ShareId, ShareError> {
        if !root.is_absolute() {
            return Err(ShareError::RelativeRoot(root));
        }
        ShareRoot::open(&root).map_err(|_| ShareError::UnusableRoot(root.clone()))?;

        let id = ShareId::generate();
        self.shares.push(Share {
            id,
            workspace_id,
            device_key: self.device_key,
            root,
            label,
            grants: Vec::new(),
        });
        self.save()?;
        Ok(id)
    }

    /// Replaces a share's access list.
    pub fn set_grants(&mut self, id: ShareId, grants: Vec<super::Grant>) -> Result<(), ShareError> {
        self.edit(id, |share| share.grants = grants)
    }

    /// Renames a share. The id does not change (r3 §3).
    pub fn set_label(&mut self, id: ShareId, label: String) -> Result<(), ShareError> {
        self.edit(id, |share| share.label = label)
    }

    /// Points a share at a different folder. The id does not change (r3 §3).
    pub fn set_root(&mut self, id: ShareId, root: PathBuf) -> Result<(), ShareError> {
        if !root.is_absolute() {
            return Err(ShareError::RelativeRoot(root));
        }
        ShareRoot::open(&root).map_err(|_| ShareError::UnusableRoot(root.clone()))?;
        self.edit(id, |share| share.root = root)
    }

    /// Stops exposing a folder.
    ///
    /// Nothing is signalled to peers and nothing in flight is hunted down. §9 is evaluated
    /// for every request, so the next operation against this id fails with
    /// [`ShareError::NoSuchShare`] and a transfer stops at its next chunk. Cancelling a
    /// transfer from here would be a second enforcement path that has to agree with the
    /// first, and the one that runs on every request is the one that cannot be forgotten.
    pub fn remove(&mut self, id: ShareId) -> Result<(), ShareError> {
        let before = self.shares.len();
        self.shares.retain(|s| s.id != id);
        if self.shares.len() == before {
            return Err(ShareError::NoSuchShare(id));
        }
        self.save()
    }

    /// What `device` is told exists, given the trust list for its workspace.
    ///
    /// A device that has just joined sees nothing until a grant covers it, which for a
    /// `workspace` grant is the moment it becomes active in the list.
    pub fn advertise_to(&self, device: &PublicKey, list: &TrustList) -> Vec<ShareDefinition> {
        self.shares
            .iter()
            .filter_map(|share| {
                share
                    .permission_for(device, list)
                    .map(|permission| share.definition_for(permission))
            })
            .collect()
    }

    /// Finds the share a request names and what the caller may do in it.
    ///
    /// The lookup behind §9 steps 5 and 6. A share the caller cannot see answers the same
    /// way as one that does not exist, so probing ids tells a peer nothing.
    pub fn resolve(
        &self,
        id: ShareId,
        device: &PublicKey,
        list: &TrustList,
    ) -> Result<(&Share, Permission), ShareError> {
        let share = self.get(id).ok_or(ShareError::NoSuchShare(id))?;
        let permission = share
            .permission_for(device, list)
            .ok_or(ShareError::NoSuchShare(id))?;
        Ok((share, permission))
    }

    fn edit(&mut self, id: ShareId, f: impl FnOnce(&mut Share)) -> Result<(), ShareError> {
        let share = self
            .shares
            .iter_mut()
            .find(|s| s.id == id)
            .ok_or(ShareError::NoSuchShare(id))?;
        f(share);
        self.save()
    }

    fn save(&self) -> Result<(), ShareError> {
        let file = ShareFile {
            version: FORMAT_VERSION,
            shares: self.shares.clone(),
        };
        let bytes = serde_json::to_vec_pretty(&file)?;
        write_atomic(&self.path, &bytes)?;
        Ok(())
    }
}

/// Why a share operation failed.
#[derive(Debug, thiserror::Error)]
pub enum ShareError {
    /// No such share, or none the caller may see.
    #[error("no share {0}")]
    NoSuchShare(ShareId),

    /// The file on disk names a share belonging to another device.
    #[error("shares.json holds a share owned by {0}, which is not this device")]
    ForeignShare(PublicKey),

    /// A share root that is not an absolute path.
    #[error("share root must be absolute: {}", .0.display())]
    RelativeRoot(PathBuf),

    /// A share root that does not open as a directory.
    #[error("cannot open {} as a share root", .0.display())]
    UnusableRoot(PathBuf),

    /// A file written by a newer build.
    #[error("shares.json is format version {0}, this build reads {FORMAT_VERSION}")]
    UnknownFormat(u32),

    /// The file is not the JSON this build expects.
    #[error("shares.json could not be parsed: {0}")]
    Malformed(#[from] serde_json::Error),

    /// Anything the filesystem reported.
    #[error(transparent)]
    Io(#[from] io::Error),
}

#[cfg(test)]
mod tests {
    use super::{
        super::tests::{entry, key, list},
        *,
    };
    use crate::{
        share::{Grant, Grantee},
        trust::Status,
    };

    struct Fixture {
        _tmp: tempfile::TempDir,
        dir: ConfigDir,
        root: PathBuf,
        store: ShareStore,
    }

    fn fixture() -> Fixture {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let root = tmp.path().join("photos");
        std::fs::create_dir(&root).unwrap();
        let store = ShareStore::open(&dir, key(0)).unwrap();
        Fixture {
            _tmp: tmp,
            dir,
            root,
            store,
        }
    }

    fn workspace_grant() -> Vec<Grant> {
        vec![Grant {
            grantee: Grantee::Workspace,
            permission: Permission::Read,
        }]
    }

    #[test]
    fn a_missing_file_is_an_empty_store() {
        assert!(fixture().store.shares().is_empty());
    }

    #[test]
    fn shares_survive_a_restart() {
        let mut f = fixture();
        let id = f
            .store
            .add(WorkspaceId::generate(), f.root.clone(), "Photos".into())
            .unwrap();
        f.store.set_grants(id, workspace_grant()).unwrap();

        let reopened = ShareStore::open(&f.dir, key(0)).unwrap();
        assert_eq!(reopened.shares(), f.store.shares());
        assert_eq!(reopened.get(id).unwrap().label, "Photos");
    }

    #[test]
    fn a_new_share_grants_nobody() {
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        let id = f.store.add(ws, f.root.clone(), "Photos".into()).unwrap();
        let device = key(1);
        let list = list(ws, vec![entry(device, None, Status::Active)]);

        assert!(f.store.advertise_to(&device, &list).is_empty());
        assert!(f.store.resolve(id, &device, &list).is_err());
    }

    #[test]
    fn the_id_survives_a_rename_and_a_move() {
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        let id = f.store.add(ws, f.root.clone(), "Photos".into()).unwrap();
        let elsewhere = f.dir.root().join("elsewhere");
        std::fs::create_dir(&elsewhere).unwrap();

        f.store.set_label(id, "Pictures".into()).unwrap();
        f.store.set_root(id, elsewhere.clone()).unwrap();
        f.store.set_grants(id, workspace_grant()).unwrap();

        let share = f.store.get(id).unwrap();
        assert_eq!(share.id, id);
        assert_eq!(share.label, "Pictures");
        assert_eq!(share.root, elsewhere);
    }

    #[test]
    fn a_relative_or_missing_root_is_refused() {
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        assert!(matches!(
            f.store.add(ws, PathBuf::from("photos"), "x".into()),
            Err(ShareError::RelativeRoot(_))
        ));
        assert!(matches!(
            f.store.add(ws, f.dir.root().join("nope"), "x".into()),
            Err(ShareError::UnusableRoot(_))
        ));
        assert!(f.store.shares().is_empty());
    }

    #[test]
    fn a_file_naming_another_device_is_refused() {
        // The wire has no path to this, but a hand-edited file, a restored backup or a
        // copied config directory does.
        let mut f = fixture();
        f.store
            .add(WorkspaceId::generate(), f.root.clone(), "Photos".into())
            .unwrap();

        let err = ShareStore::open(&f.dir, key(7)).unwrap_err();
        assert!(matches!(err, ShareError::ForeignShare(_)), "{err:?}");
    }

    #[test]
    fn a_removed_share_stops_answering() {
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        let id = f.store.add(ws, f.root.clone(), "Photos".into()).unwrap();
        f.store.set_grants(id, workspace_grant()).unwrap();
        let device = key(1);
        let list = list(ws, vec![entry(device, None, Status::Active)]);
        assert_eq!(f.store.advertise_to(&device, &list).len(), 1);

        f.store.remove(id).unwrap();
        assert!(f.store.advertise_to(&device, &list).is_empty());
        assert!(matches!(
            f.store.resolve(id, &device, &list),
            Err(ShareError::NoSuchShare(_))
        ));
        assert!(matches!(
            f.store.remove(id),
            Err(ShareError::NoSuchShare(_))
        ));
    }

    #[test]
    fn a_share_the_caller_cannot_see_answers_like_one_that_is_not_there() {
        // Otherwise a peer learns which ids exist by watching which error comes back.
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        let id = f.store.add(ws, f.root.clone(), "Photos".into()).unwrap();
        let stranger = key(9);
        let list = list(ws, vec![entry(stranger, None, Status::Active)]);

        let hidden = f.store.resolve(id, &stranger, &list).unwrap_err();
        let absent = f
            .store
            .resolve(ShareId::generate(), &stranger, &list)
            .unwrap_err();
        assert_eq!(hidden.to_string(), format!("no share {id}"));
        assert!(matches!(absent, ShareError::NoSuchShare(_)));
    }

    #[test]
    fn revoking_a_device_removes_its_access_and_touches_no_share() {
        // #10's acceptance criterion, from this side. Revocation is a new trust-list
        // version and nothing else; if it needed every share on every device edited too,
        // it would be slow, partial, and wrong the moment one device was offline.
        let mut f = fixture();
        let ws = WorkspaceId::generate();
        let id = f.store.add(ws, f.root.clone(), "Photos".into()).unwrap();
        f.store.set_grants(id, workspace_grant()).unwrap();

        let device = key(1);
        let active = list(ws, vec![entry(device, None, Status::Active)]);
        assert_eq!(f.store.advertise_to(&device, &active).len(), 1);

        let on_disk = std::fs::read(f.dir.shares_file()).unwrap();
        let revoked = list(ws, vec![entry(device, None, Status::Revoked)]);
        assert!(f.store.advertise_to(&device, &revoked).is_empty());
        assert!(f.store.resolve(id, &device, &revoked).is_err());
        assert_eq!(
            std::fs::read(f.dir.shares_file()).unwrap(),
            on_disk,
            "revocation rewrote a share definition"
        );
    }

    #[test]
    fn shares_in_another_workspace_are_not_advertised() {
        let mut f = fixture();
        let (mine, theirs) = (WorkspaceId::generate(), WorkspaceId::generate());
        let id = f.store.add(mine, f.root.clone(), "Photos".into()).unwrap();
        f.store.set_grants(id, workspace_grant()).unwrap();

        let device = key(1);
        let other_list = list(theirs, vec![entry(device, None, Status::Active)]);
        assert!(f.store.advertise_to(&device, &other_list).is_empty());
    }

    #[test]
    fn a_file_from_a_newer_build_is_refused_rather_than_half_read() {
        let f = fixture();
        std::fs::write(f.dir.shares_file(), br#"{"version":99,"shares":[]}"#).unwrap();
        let err = ShareStore::open(&f.dir, key(0)).unwrap_err();
        assert!(matches!(err, ShareError::UnknownFormat(99)), "{err:?}");
    }
}
