//! Workspace lifecycle and the mutation helpers later issues call.
//!
//! r3 section 14. A workspace is created, lives local-only, can gain remote access
//! through one account, can be suspended when payment lapses, and can be deleted. The
//! workspace id is constant through every state. Deletion ends that: the id is gone
//! with the list.
//!
//! The lifecycle states here are this device's local view. The cloud keeps its own
//! association rows (#22). The trust list itself only orders membership; association
//! state rides alongside it in a sidecar file.
//!
//! Deletion is a final signed version with `status=deleted` at version N+1. It goes
//! through the same acceptance check as any other version. Agents that receive it
//! drop their copy of the list. User files and device keys are untouched.
//!
//! The sidecar keeps three states unsigned: local-only, cloud-associated, suspended.
//! Those describe this device's view of its cloud association. No peer needs to
//! agree with them, so no signature covers them. Deleted ends membership for every
//! device, so every device must be able to prove it. That is why deleted is signed
//! into the list and the other three are not.

use std::{io, path::PathBuf};

use iroh::{PublicKey, SecretKey};

use super::{
    Entry, Role, SignedTrustList, Status, StoreError, TrustList, TrustStore, WorkspaceId,
    WorkspaceStatus,
    accept::{Decision, Reject, decide, is_active_admin},
};
use crate::config::{ConfigDir, write_atomic};

/// This device's local view of where a workspace stands in r3 section 14.
///
/// `Deleted` is absent on purpose. Deleting consumes the workspace and records a
/// marker with the deleted version. There is no value of this enum that means
/// deleted because there is no workspace left to hold one.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum State {
    /// Created, never associated with an account.
    LocalOnly,
    /// Remote access is on under one account.
    CloudAssociated,
    /// Payment lapsed. Relays refuse. Local paths work. Membership is kept.
    Suspended,
}

impl State {
    fn as_word(self) -> &'static str {
        match self {
            State::LocalOnly => "local-only",
            State::CloudAssociated => "cloud-associated",
            State::Suspended => "suspended",
        }
    }

    fn parse(word: &str) -> Option<Self> {
        match word {
            "local-only" => Some(State::LocalOnly),
            "cloud-associated" => Some(State::CloudAssociated),
            "suspended" => Some(State::Suspended),
            _ => None,
        }
    }
}

/// A workspace held by this device: the accepted trust list plus lifecycle state.
#[derive(Debug)]
pub struct Workspace {
    state: State,
    held: SignedTrustList,
    store: TrustStore,
    dir: PathBuf,
    deleted: bool,
}

impl Workspace {
    /// Creates a workspace. The creator is its first admin, active, unbound.
    ///
    /// The first version is 1, self-signed by the creating device. There is no other
    /// root of trust.
    pub fn create(
        config: &ConfigDir,
        name: String,
        creator: &SecretKey,
        display_name: String,
    ) -> Result<Self, Error> {
        let id = WorkspaceId::generate();
        let list = TrustList {
            workspace_id: id,
            name,
            version: 1,
            status: WorkspaceStatus::Live,
            entries: vec![Entry {
                device_key: creator.public(),
                display_name,
                role: Role::Admin,
                account_id: None,
                status: Status::Active,
            }],
        };
        let signed = list.sign(creator);
        let store = TrustStore::new(config);
        store.save(&signed)?;
        let this = Self {
            state: State::LocalOnly,
            held: signed,
            store,
            dir: config.trust_dir(),
            deleted: false,
        };
        this.save_state()?;
        Ok(this)
    }

    /// The workspace id. Constant in every state. Deletion drops it with the list.
    pub fn id(&self) -> WorkspaceId {
        self.held.list().workspace_id
    }

    /// The local lifecycle view.
    pub fn state(&self) -> State {
        self.state
    }

    /// The trust list version currently held.
    pub fn held(&self) -> &SignedTrustList {
        &self.held
    }

    /// Records that remote access was enabled under one account.
    pub fn enable_remote_access(&mut self) -> Result<(), Error> {
        self.step(State::LocalOnly, State::CloudAssociated)
    }

    /// Records that the owner's subscription lapsed past grace.
    pub fn note_subscription_suspended(&mut self) -> Result<(), Error> {
        self.step(State::CloudAssociated, State::Suspended)
    }

    /// Records that the subscription renewed.
    pub fn renew_remote_access(&mut self) -> Result<(), Error> {
        self.step(State::Suspended, State::CloudAssociated)
    }

    /// Records that remote access was disabled. Local operation continues.
    pub fn disable_remote_access(&mut self) -> Result<(), Error> {
        self.live()?;
        match self.state {
            State::CloudAssociated | State::Suspended => self.set_state(State::LocalOnly),
            State::LocalOnly => Err(Error::BadTransition {
                from: self.state,
                to: State::LocalOnly,
            }),
        }
    }

    fn step(&mut self, from: State, to: State) -> Result<(), Error> {
        self.live()?;
        if self.state != from {
            return Err(Error::BadTransition {
                from: self.state,
                to,
            });
        }
        self.set_state(to)
    }

    fn set_state(&mut self, state: State) -> Result<(), Error> {
        self.state = state;
        self.save_state()?;
        Ok(())
    }

    fn save_state(&self) -> Result<(), Error> {
        write_atomic(&self.state_path(), self.state.as_word().as_bytes())?;
        Ok(())
    }

    fn state_path(&self) -> PathBuf {
        self.dir.join(format!("{}.state", self.id()))
    }

    /// Takes a candidate list received from any path (LAN, cloud, connection).
    ///
    /// Runs the section 8.1 rule and saves on accept. Rejections leave the held
    /// list alone. Accepting a deleted version drops the stored list and records
    /// a marker with its version, so older versions cannot come back.
    pub fn apply(&mut self, candidate: SignedTrustList) -> Result<ApplyOutcome, Error> {
        self.live()?;
        match decide(Some(&self.held), &candidate)? {
            Decision::Accept if candidate.list().status == WorkspaceStatus::Deleted => {
                let version = candidate.list().version;
                write_atomic(
                    &deleted_marker_path(&self.dir, self.id()),
                    &marker_bytes(version),
                )?;
                remove_if_present(self.dir.join(format!("{}.tl", self.id())))?;
                remove_if_present(self.state_path())?;
                self.held = candidate;
                self.deleted = true;
                Ok(ApplyOutcome::Deleted)
            }
            Decision::Accept => {
                self.store.save(&candidate)?;
                self.held = candidate;
                Ok(ApplyOutcome::Updated)
            }
            Decision::Duplicate => Ok(ApplyOutcome::Duplicate),
        }
    }

    /// Adds a device as standard with no account binding. For local pairing (#17).
    pub fn add_device(
        &mut self,
        signer: &SecretKey,
        device_key: PublicKey,
        display_name: String,
    ) -> Result<(), Error> {
        self.mutate(signer, |list| {
            if list.entry(&device_key).is_some() {
                return Err(Error::AlreadyMember(device_key));
            }
            list.entries.push(Entry {
                device_key,
                display_name,
                role: Role::Standard,
                account_id: None,
                status: Status::Active,
            });
            Ok(())
        })
    }

    /// Marks an entry revoked. Peers enforce it at connection time (#10).
    pub fn revoke_device(
        &mut self,
        signer: &SecretKey,
        device_key: &PublicKey,
    ) -> Result<(), Error> {
        self.mutate(signer, |list| {
            let slot = list
                .entries
                .iter_mut()
                .find(|e| e.device_key == *device_key)
                .ok_or(Error::UnknownDevice(*device_key))?;
            slot.status = Status::Revoked;
            Ok(())
        })
    }

    /// Changes an entry's role.
    pub fn set_role(
        &mut self,
        signer: &SecretKey,
        device_key: &PublicKey,
        role: Role,
    ) -> Result<(), Error> {
        self.mutate(signer, |list| {
            let slot = list
                .entries
                .iter_mut()
                .find(|e| e.device_key == *device_key)
                .ok_or(Error::UnknownDevice(*device_key))?;
            slot.role = role;
            Ok(())
        })
    }

    /// Sets or clears the account an entry was admitted under.
    pub fn set_account(
        &mut self,
        signer: &SecretKey,
        device_key: &PublicKey,
        account_id: Option<String>,
    ) -> Result<(), Error> {
        self.mutate(signer, |list| {
            let slot = list
                .entries
                .iter_mut()
                .find(|e| e.device_key == *device_key)
                .ok_or(Error::UnknownDevice(*device_key))?;
            slot.account_id = account_id;
            Ok(())
        })
    }

    /// One mutation step: check the signer, apply the edit, sign version N+1, save.
    ///
    /// Refused when this device is not an active admin in the held version. Guarding
    /// the last admin against revocation or demotion is #11 and lives there.
    fn mutate(
        &mut self,
        signer: &SecretKey,
        edit: impl FnOnce(&mut TrustList) -> Result<(), Error>,
    ) -> Result<(), Error> {
        self.live()?;
        let key = signer.public();
        if !is_active_admin(self.held.list(), &key) {
            return Err(Error::NotAdmin(key));
        }
        let mut next = self.held.list().clone();
        next.version = next.version.checked_add(1).ok_or(Error::VersionOverflow)?;
        edit(&mut next)?;
        let signed = next.sign(signer);
        self.store.save(&signed)?;
        self.held = signed;
        Ok(())
    }

    /// Deletes the workspace. Consumes it; there is no workspace left afterwards.
    ///
    /// Produces the final trust list at version N+1 with `status=deleted`, signed
    /// by this device. The caller sends it to peers; on receipt they drop their
    /// copy. Locally the list file is dropped and a marker records the version,
    /// so older versions cannot come back. Refused when this device is not an
    /// active admin.
    pub fn delete(self, signer: &SecretKey) -> Result<SignedTrustList, Error> {
        let key = signer.public();
        if !is_active_admin(self.held.list(), &key) {
            return Err(Error::NotAdmin(key));
        }
        let mut next = self.held.list().clone();
        next.version = next.version.checked_add(1).ok_or(Error::VersionOverflow)?;
        next.status = WorkspaceStatus::Deleted;
        let signed = next.sign(signer);
        write_atomic(
            &deleted_marker_path(&self.dir, self.id()),
            &marker_bytes(signed.list().version),
        )?;
        remove_if_present(self.dir.join(format!("{}.tl", self.id())))?;
        remove_if_present(self.state_path())?;
        Ok(signed)
    }

    /// Refuses work on a workspace that already accepted its deleted version.
    fn live(&self) -> Result<(), Error> {
        if self.deleted {
            return Err(Error::WorkspaceDeleted);
        }
        Ok(())
    }
}

fn remove_if_present(path: PathBuf) -> Result<(), Error> {
    match std::fs::remove_file(&path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(Error::Io(e)),
    }
}

/// What is on disk for one workspace id.
#[derive(Debug)]
pub enum Stored {
    /// A live workspace. Open it and continue. Boxed: it holds the full list
    /// and dwarfs the other variants.
    Active(Box<Workspace>),
    /// A deleted workspace, with the version the delete was recorded at. Every
    /// version of the id is refused from here.
    Deleted(u64),
    /// Nothing held for this id.
    Absent,
}

/// Loads what this device holds for `id`: workspace, deletion marker, or nothing.
///
/// A marker wins over a list file. Both present means the delete was recorded and
/// the list file is a leftover; the workspace stays deleted. A deleted list file
/// with no marker repairs itself into the same state.
pub fn load(config: &ConfigDir, id: WorkspaceId) -> Result<Stored, Error> {
    let dir = config.trust_dir();
    if let Some(bytes) = read_if_present(deleted_marker_path(&dir, id))? {
        let version = parse_marker(&bytes).ok_or(Error::BadDeletedMarker)?;
        remove_if_present(dir.join(format!("{id}.tl")))?;
        remove_if_present(dir.join(format!("{id}.state")))?;
        return Ok(Stored::Deleted(version));
    }
    let store = TrustStore::new(config);
    if !store.contains(id) {
        return Ok(Stored::Absent);
    }
    let held = store.load(id)?;
    if held.list().status == WorkspaceStatus::Deleted {
        let version = held.list().version;
        write_atomic(&deleted_marker_path(&dir, id), &marker_bytes(version))?;
        remove_if_present(dir.join(format!("{id}.tl")))?;
        remove_if_present(dir.join(format!("{id}.state")))?;
        return Ok(Stored::Deleted(version));
    }
    let state = match std::fs::read(dir.join(format!("{id}.state"))) {
        Ok(bytes) => {
            let word = String::from_utf8(bytes).map_err(|_| Error::BadStateFile)?;
            State::parse(word.trim()).ok_or(Error::BadStateFile)?
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => State::LocalOnly,
        Err(e) => return Err(Error::Io(e)),
    };
    Ok(Stored::Active(Box::new(Workspace {
        state,
        held,
        store,
        dir,
        deleted: false,
    })))
}

fn deleted_marker_path(dir: &std::path::Path, id: WorkspaceId) -> PathBuf {
    dir.join(format!("{id}.deleted"))
}

fn marker_bytes(version: u64) -> Vec<u8> {
    format!("rfm-deleted {version}\n").into_bytes()
}

fn parse_marker(bytes: &[u8]) -> Option<u64> {
    let text = std::str::from_utf8(bytes).ok()?;
    text.strip_prefix("rfm-deleted ")?
        .strip_suffix('\n')?
        .parse()
        .ok()
}

fn read_if_present(path: PathBuf) -> Result<Option<Vec<u8>>, Error> {
    match std::fs::read(&path) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(Error::Io(e)),
    }
}

/// What went wrong in a workspace operation.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("this device is not an active admin in the held version")]
    NotAdmin(PublicKey),
    #[error("no entry carries that device key")]
    UnknownDevice(PublicKey),
    #[error("that device key is already in the workspace")]
    AlreadyMember(PublicKey),
    #[error("the version counter cannot advance past u64::MAX")]
    VersionOverflow,
    #[error("that step is not valid from this state")]
    BadTransition { from: State, to: State },
    #[error("the lifecycle sidecar file does not parse")]
    BadStateFile,
    #[error("the deletion marker file does not parse")]
    BadDeletedMarker,
    #[error("the workspace already accepted its deleted version")]
    WorkspaceDeleted,
    #[error("a trust list was refused")]
    Rejected(#[from] Reject),
    #[error("the trust store failed")]
    Store(#[from] StoreError),
    #[error("disk I/O failed")]
    Io(#[from] io::Error),
}

/// What [`Workspace::apply`] did with a candidate list.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ApplyOutcome {
    /// The candidate passed the rule and is now held.
    Updated,
    /// The candidate was the held list already. Nothing changed.
    Duplicate,
    /// The candidate was the signed deleted version. The list file is dropped
    /// and a marker records the version.
    Deleted,
}

#[cfg(test)]
mod tests {
    use iroh::SecretKey;

    use super::*;
    use crate::trust::accept::decide;

    fn setup(name: &str) -> (tempfile::TempDir, ConfigDir, SecretKey) {
        let tmp = tempfile::tempdir().unwrap();
        let config = ConfigDir::open(tmp.path().join(name)).unwrap();
        let admin = SecretKey::generate();
        (tmp, config, admin)
    }

    fn create(config: &ConfigDir, admin: &SecretKey) -> Workspace {
        Workspace::create(config, "home".to_owned(), admin, "laptop".to_owned()).unwrap()
    }

    #[test]
    fn create_makes_a_self_signed_genesis_in_local_only() {
        let (_tmp, config, admin) = setup("create");
        let ws = create(&config, &admin);

        assert_eq!(ws.state(), State::LocalOnly);
        assert_eq!(ws.held().list().version, 1);
        assert!(is_active_admin(ws.held().list(), &admin.public()));
        ws.held().verify().unwrap();
        assert_eq!(
            decide(None, ws.held()),
            Ok(Decision::Accept),
            "genesis must pass the acceptance rule with no held version"
        );
    }

    #[test]
    fn lifecycle_steps_keep_the_workspace_id() {
        let (_tmp, config, admin) = setup("lifecycle");
        let mut ws = create(&config, &admin);
        let id = ws.id();

        ws.enable_remote_access().unwrap();
        assert_eq!(ws.state(), State::CloudAssociated);
        ws.note_subscription_suspended().unwrap();
        assert_eq!(ws.state(), State::Suspended);
        ws.renew_remote_access().unwrap();
        assert_eq!(ws.state(), State::CloudAssociated);
        ws.disable_remote_access().unwrap();
        assert_eq!(ws.state(), State::LocalOnly);
        assert_eq!(ws.id(), id, "the id stays constant through every state");

        assert!(ws.enable_remote_access().is_ok());
        assert!(
            ws.enable_remote_access().is_err(),
            "enabling twice must fail"
        );
        assert!(
            ws.renew_remote_access().is_err(),
            "renewing outside suspended must fail"
        );
    }

    #[test]
    fn mutations_bump_the_version_and_need_an_admin() {
        let (_tmp, config, admin) = setup("mutations");
        let mut ws = create(&config, &admin);
        let member = SecretKey::generate();

        ws.add_device(&admin, member.public(), "phone".to_owned())
            .unwrap();
        assert_eq!(ws.held().list().version, 2);
        ws.held().verify().unwrap();

        ws.set_account(&admin, &member.public(), Some("acct_9".to_owned()))
            .unwrap();
        assert_eq!(ws.held().list().version, 3);

        ws.set_role(&admin, &member.public(), Role::Admin).unwrap();
        assert_eq!(ws.held().list().version, 4);

        ws.revoke_device(&admin, &member.public()).unwrap();
        assert_eq!(ws.held().list().version, 5);

        let outsider = SecretKey::generate();
        assert!(
            matches!(
                ws.add_device(&outsider, SecretKey::generate().public(), "x".to_owned()),
                Err(Error::NotAdmin(_))
            ),
            "a device that is not an active admin must be refused"
        );
        let standard = SecretKey::generate();
        ws.add_device(&admin, standard.public(), "tablet".to_owned())
            .unwrap();
        assert!(
            matches!(
                ws.revoke_device(&standard, &member.public()),
                Err(Error::NotAdmin(_))
            ),
            "a standard device must be refused"
        );
        assert_eq!(
            ws.held().list().version,
            6,
            "refused mutations must not advance the version"
        );
    }

    #[test]
    fn apply_takes_a_signed_next_version() {
        let (_tmp, config, admin) = setup("apply");
        let mut ws = create(&config, &admin);

        let mut next = ws.held().list().clone();
        next.version = 2;
        let candidate = next.sign(&admin);
        assert!(matches!(ws.apply(candidate), Ok(ApplyOutcome::Updated)));
        assert_eq!(ws.held().list().version, 2);
        assert!(matches!(
            ws.apply(ws.held().clone()),
            Ok(ApplyOutcome::Duplicate)
        ));
    }

    #[test]
    fn delete_returns_a_signed_deleted_version_and_bars_everything_after() {
        let (_tmp, config, admin) = setup("delete");
        let ws = create(&config, &admin);
        let id = ws.id();

        let old_bytes = ws.held().encode();
        let old_version = ws.held().list().version;
        let final_list = ws.delete(&admin).unwrap();
        assert_eq!(final_list.list().workspace_id, id);
        assert_eq!(final_list.list().version, old_version + 1);
        assert_eq!(final_list.list().status, WorkspaceStatus::Deleted);
        final_list.verify().unwrap();

        let dir = config.trust_dir();
        assert!(
            !dir.join(format!("{id}.tl")).exists(),
            "the list file is dropped on delete"
        );
        assert!(
            dir.join(format!("{id}.deleted")).exists(),
            "the marker is recorded so the delete survives restart"
        );

        assert!(
            matches!(load(&config, id).unwrap(), Stored::Deleted(v) if v == old_version + 1),
            "loading a deleted id reports the marker"
        );

        // The deleting device's own receipt path: the final version passes the rule
        // against what it held, then there is nothing left to apply to.
        let old = SignedTrustList::decode(&old_bytes).unwrap();
        assert_eq!(
            decide(Some(&old), &final_list),
            Ok(Decision::Accept),
            "the final version goes through the same check as any other"
        );
        assert_eq!(
            decide(Some(&final_list), &old),
            Err(Reject::Deleted),
            "replaying the version held before deletion must fail"
        );

        let mut higher = old.list().clone();
        higher.version = old_version + 5;
        let forged_future = higher.sign(&admin);
        assert_eq!(
            decide(Some(&final_list), &forged_future),
            Err(Reject::Deleted),
            "a higher version signed after deletion must fail too"
        );

        assert!(
            matches!(load(&config, id).unwrap(), Stored::Deleted(_)),
            "no replay can bring the workspace back"
        );
    }

    #[test]
    fn apply_drops_the_list_on_a_signed_delete() {
        let (_tmp, config, admin) = setup("apply-delete");
        let mut ws = create(&config, &admin);
        let id = ws.id();

        let mut next = ws.held().list().clone();
        next.version = 2;
        next.status = WorkspaceStatus::Deleted;
        let candidate = next.sign(&admin);
        assert!(matches!(ws.apply(candidate), Ok(ApplyOutcome::Deleted)));

        let dir = config.trust_dir();
        assert!(!dir.join(format!("{id}.tl")).exists());
        assert!(matches!(load(&config, id).unwrap(), Stored::Deleted(2)));
        assert!(
            matches!(
                ws.add_device(&admin, SecretKey::generate().public(), "x".to_owned()),
                Err(Error::WorkspaceDeleted)
            ),
            "a workspace that accepted its delete takes no further mutations"
        );
    }

    #[test]
    fn deletion_marker_round_trips_through_disk() {
        let (_tmp, config, admin) = setup("marker-disk");
        let ws = create(&config, &admin);
        let id = ws.id();
        let final_list = ws.delete(&admin).unwrap();

        let bytes = std::fs::read(config.trust_dir().join(format!("{id}.deleted"))).unwrap();
        assert_eq!(parse_marker(&bytes), Some(final_list.list().version));
    }
}
