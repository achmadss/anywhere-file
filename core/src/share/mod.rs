//! Shares: the unit of permission (r3 §12).
//!
//! A share is a folder one device exposes, plus a list of who may touch it. There are no
//! per-file permissions in v1, so this list and the trust list together answer every
//! question about who may read or write what.
//!
//! This module answers §9 step 6, "does share S grant device K the permission it asked
//! for". Step 5, whether the path is inside the share at all, is [`crate::fs`]'s. The
//! whole seven-step rule is assembled in #14.
//!
//! Shares live on the device that owns the folder and nowhere else. Nothing here takes a
//! share definition from a peer, and [`ShareStore`] refuses to hold one that names another
//! device, so "configure my neighbour's shares" is not an operation that exists.

mod store;

use std::path::PathBuf;

use iroh::PublicKey;
use serde::{Deserialize, Serialize};

use crate::trust::{Status, TrustList, WorkspaceId};

pub use self::store::{ShareError, ShareStore};

/// A share identifier: random 128 bits, minted when the share is created.
///
/// Independent of every other identity (r3 §3). Renaming a share, moving its root, or
/// changing its grants leaves it alone, so a transfer in flight and a bookmark in the UI
/// both keep pointing at the same thing.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(into = "String", try_from = "String")]
pub struct ShareId([u8; 16]);

impl ShareId {
    /// Mints a new identifier from the operating system's CSPRNG.
    pub fn generate() -> Self {
        let mut bytes = [0u8; 16];
        getrandom::fill(&mut bytes).expect("the OS CSPRNG is unavailable");
        Self(bytes)
    }

    /// The raw 16 bytes.
    pub fn as_bytes(&self) -> &[u8; 16] {
        &self.0
    }
}

impl std::fmt::Display for ShareId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        for byte in self.0 {
            write!(f, "{byte:02x}")?;
        }
        Ok(())
    }
}

impl std::str::FromStr for ShareId {
    type Err = IdParseError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Ok(Self(parse_hex16(s)?))
    }
}

impl From<ShareId> for String {
    fn from(id: ShareId) -> Self {
        id.to_string()
    }
}

impl TryFrom<String> for ShareId {
    type Error = IdParseError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        s.parse()
    }
}

/// A 128-bit identifier that was not 32 hex characters.
#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
#[error("expected 32 hex characters")]
pub struct IdParseError;

pub(crate) fn parse_hex16(s: &str) -> Result<[u8; 16], IdParseError> {
    if s.len() != 32 {
        return Err(IdParseError);
    }
    let mut out = [0u8; 16];
    for (i, byte) in out.iter_mut().enumerate() {
        *byte = u8::from_str_radix(&s[i * 2..i * 2 + 2], 16).map_err(|_| IdParseError)?;
    }
    Ok(out)
}

/// What a grantee may do. Ordered, so the strongest of several grants wins.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Permission {
    /// List, stat and read.
    Read,
    /// Everything, including write, delete, move and mkdir.
    ReadWrite,
}

/// Who a grant is for.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case", tag = "kind")]
pub enum Grantee {
    /// Every active device in the share's workspace, including ones admitted later.
    Workspace,
    /// One device, whether or not an account is behind it.
    Device {
        /// The device's public key.
        key: PublicKey,
    },
    /// Every device whose trust-list entry is bound to this account (r3 §8.3).
    Account {
        /// The cloud account id.
        id: String,
    },
}

impl Grantee {
    /// Whether this grantee covers `device`, given its trust-list entry.
    fn covers(&self, device: &PublicKey, entry: &crate::trust::Entry) -> bool {
        match self {
            Self::Workspace => true,
            Self::Device { key } => key == device,
            Self::Account { id } => entry.account_id.as_deref() == Some(id.as_str()),
        }
    }
}

/// One line of a share's access list.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Grant {
    /// Who it is for.
    pub grantee: Grantee,
    /// What they may do.
    pub permission: Permission,
}

/// A folder this device exposes to one workspace.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Share {
    /// Stable for the life of the share.
    pub id: ShareId,
    /// Which workspace the grants are read against.
    ///
    /// Not in r3 §12's diagram, and needed because §11.1 puts one device in several
    /// workspaces at once. Without it a grant to `workspace` would mean "every workspace I
    /// happen to belong to", and a folder shared with the family would appear at work.
    pub workspace_id: WorkspaceId,
    /// The device that owns the folder. Always this device; see the module docs.
    pub device_key: PublicKey,
    /// Absolute path on the owning device.
    pub root: PathBuf,
    /// A label for humans. Carries no authority and is not an identifier.
    pub label: String,
    /// Who may do what. An empty list is a share nobody outside this device can see.
    pub grants: Vec<Grant>,
}

impl Share {
    /// What `device` may do here, or `None` if it may not see the share at all.
    ///
    /// r3 §9 step 6. `list` must be the trust list for this share's workspace; a list for
    /// another workspace answers `None` rather than being taken at face value.
    ///
    /// A revoked entry gets nothing, including through a `device:` grant naming it: #10
    /// revokes by trust-list version, so a revocation must not need every share on every
    /// device to be edited before it takes effect.
    pub fn permission_for(&self, device: &PublicKey, list: &TrustList) -> Option<Permission> {
        if list.workspace_id != self.workspace_id {
            return None;
        }
        let entry = list.entry(device)?;
        if entry.status != Status::Active {
            return None;
        }
        self.grants
            .iter()
            .filter(|grant| grant.grantee.covers(device, entry))
            .map(|grant| grant.permission)
            .max()
    }

    /// What one device is told this share is.
    pub fn definition_for(&self, permission: Permission) -> ShareDefinition {
        ShareDefinition {
            id: self.id,
            device_key: self.device_key,
            label: self.label.clone(),
            permission,
        }
    }
}

/// A share as another device sees it.
///
/// Carries no `root`. The absolute path is the owning machine's business, it says where a
/// person keeps their files and often who they are, and a peer needs only the id to ask
/// for anything inside. Also no grant list: a device is told what *it* may do and not who
/// else is on the list.
///
/// Sent at connection time on the `rfm/1` stream, in a frame #15 defines. The frame is
/// per-connection because the peer key is what decides the contents, and the connection is
/// where that key is authenticated.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ShareDefinition {
    /// The share's stable identifier.
    pub id: ShareId,
    /// The device to ask.
    pub device_key: PublicKey,
    /// A label for humans.
    pub label: String,
    /// What the device being told may do.
    pub permission: Permission,
}

#[cfg(test)]
mod tests {
    use iroh::SecretKey;

    use super::*;
    use crate::trust::{Entry, Role};

    pub(super) fn key(seed: u8) -> PublicKey {
        SecretKey::from_bytes(&[seed; 32]).public()
    }

    pub(super) fn list(workspace: WorkspaceId, entries: Vec<Entry>) -> TrustList {
        TrustList {
            workspace_id: workspace,
            name: "test".into(),
            version: 1,
            entries,
        }
    }

    pub(super) fn entry(device: PublicKey, account: Option<&str>, status: Status) -> Entry {
        Entry {
            device_key: device,
            display_name: "device".into(),
            role: Role::Standard,
            account_id: account.map(str::to_owned),
            status,
        }
    }

    pub(super) fn share(workspace: WorkspaceId, grants: Vec<Grant>) -> Share {
        Share {
            id: ShareId::generate(),
            workspace_id: workspace,
            device_key: key(0),
            root: PathBuf::from("/srv/photos"),
            label: "Photos".into(),
            grants,
        }
    }

    fn grant(grantee: Grantee, permission: Permission) -> Grant {
        Grant {
            grantee,
            permission,
        }
    }

    #[test]
    fn a_workspace_grant_covers_every_active_device() {
        let ws = WorkspaceId::generate();
        let (a, b) = (key(1), key(2));
        let list = list(
            ws,
            vec![
                entry(a, None, Status::Active),
                entry(b, None, Status::Active),
            ],
        );
        let share = share(ws, vec![grant(Grantee::Workspace, Permission::Read)]);

        assert_eq!(share.permission_for(&a, &list), Some(Permission::Read));
        assert_eq!(share.permission_for(&b, &list), Some(Permission::Read));
    }

    #[test]
    fn a_device_outside_the_trust_list_gets_nothing() {
        let ws = WorkspaceId::generate();
        let list = list(ws, vec![entry(key(1), None, Status::Active)]);
        let share = share(ws, vec![grant(Grantee::Workspace, Permission::ReadWrite)]);

        assert_eq!(share.permission_for(&key(9), &list), None);
    }

    #[test]
    fn a_revoked_device_gets_nothing_even_when_named() {
        let ws = WorkspaceId::generate();
        let revoked = key(1);
        let list = list(ws, vec![entry(revoked, None, Status::Revoked)]);
        let share = share(
            ws,
            vec![grant(
                Grantee::Device { key: revoked },
                Permission::ReadWrite,
            )],
        );

        // #10 revokes by publishing a trust-list version. If a `device:` grant survived
        // that, revocation would mean editing every share on every device.
        assert_eq!(share.permission_for(&revoked, &list), None);
    }

    #[test]
    fn an_account_grant_follows_the_trust_list_binding() {
        let ws = WorkspaceId::generate();
        let (bound, unbound, other) = (key(1), key(2), key(3));
        let list = list(
            ws,
            vec![
                entry(bound, Some("acct-1"), Status::Active),
                entry(unbound, None, Status::Active),
                entry(other, Some("acct-2"), Status::Active),
            ],
        );
        let share = share(
            ws,
            vec![grant(
                Grantee::Account {
                    id: "acct-1".into(),
                },
                Permission::Read,
            )],
        );

        assert_eq!(share.permission_for(&bound, &list), Some(Permission::Read));
        assert_eq!(share.permission_for(&unbound, &list), None);
        assert_eq!(share.permission_for(&other, &list), None);
    }

    #[test]
    fn the_strongest_matching_grant_wins() {
        let ws = WorkspaceId::generate();
        let device = key(1);
        let list = list(ws, vec![entry(device, Some("acct-1"), Status::Active)]);
        let share = share(
            ws,
            vec![
                grant(Grantee::Workspace, Permission::Read),
                grant(Grantee::Device { key: device }, Permission::ReadWrite),
                grant(
                    Grantee::Account {
                        id: "acct-1".into(),
                    },
                    Permission::Read,
                ),
            ],
        );

        assert_eq!(
            share.permission_for(&device, &list),
            Some(Permission::ReadWrite)
        );
    }

    #[test]
    fn a_trust_list_for_another_workspace_grants_nothing() {
        // A device in two workspaces holds two lists. Reading a share against the wrong
        // one would hand the family's folder to a colleague.
        let device = key(1);
        let mine = WorkspaceId::generate();
        let theirs = WorkspaceId::generate();
        let share = share(mine, vec![grant(Grantee::Workspace, Permission::ReadWrite)]);
        let other_list = list(theirs, vec![entry(device, None, Status::Active)]);

        assert_eq!(share.permission_for(&device, &other_list), None);
    }

    #[test]
    fn a_share_with_no_grants_is_invisible() {
        let ws = WorkspaceId::generate();
        let device = key(1);
        let list = list(ws, vec![entry(device, None, Status::Active)]);
        assert_eq!(share(ws, vec![]).permission_for(&device, &list), None);
    }

    #[test]
    fn the_advertised_definition_leaves_the_path_behind() {
        let ws = WorkspaceId::generate();
        let share = share(ws, vec![grant(Grantee::Workspace, Permission::Read)]);
        let definition = share.definition_for(Permission::Read);

        let json = serde_json::to_string(&definition).unwrap();
        assert!(!json.contains("/srv/photos"), "{json}");
        assert!(!json.contains("grants"), "{json}");
        assert_eq!(definition.id, share.id);
    }

    #[test]
    fn share_ids_round_trip_through_text() {
        let id = ShareId::generate();
        assert_eq!(id.to_string().parse::<ShareId>().unwrap(), id);
        assert!("nonsense".parse::<ShareId>().is_err());
        assert!("00".parse::<ShareId>().is_err());
    }
}
