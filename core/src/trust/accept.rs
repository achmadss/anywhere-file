//! The r3 section 8.1 acceptance rule.
//!
//! A device holds one trust list per workspace. A new list replaces the held one only if
//! the key that signed it was `active` and `admin` in the held version. The first version
//! is self-signed by the creating device. There is no other root of trust.
//!
//! [`decide`] answers "should this list replace the one held". It checks the signature
//! first, then the signer against the held version, then the version number. The caller
//! persists the result. See [`crate::trust::Workspace`] for the caller that does this.
//!
//! A joining device with no held version cannot use this rule past genesis. Trusting a
//! later version on first contact is pairing (#17), which authenticates the sender through
//! the one-time secret. This module only anchors genesis.

use iroh::PublicKey;

use super::{SignedTrustList, TrustList, VerifyError, WorkspaceStatus};

/// Whether `key` may sign the next version after `held`.
///
/// This is the authority check in the rule. The caller passes the held version; the
/// candidate version is never consulted here.
pub fn is_active_admin(held: &TrustList, key: &PublicKey) -> bool {
    held.entry(key)
        .is_some_and(|e| e.role == super::Role::Admin && e.status == super::Status::Active)
}

/// What [`decide`] concluded about a candidate list.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Decision {
    /// The candidate replaces the held list. The caller saves it.
    Accept,
    /// The candidate is byte for byte the list already held. Nothing changes.
    Duplicate,
}

/// Why a candidate list was refused.
///
/// Every variant leaves the held list in place. Refusal is the safe default: a list that
/// cannot be placed in the chain described above is kept out.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Reject {
    #[error("the candidate is for another workspace")]
    WorkspaceMismatch,
    #[error("refusing to judge a list that does not verify")]
    BadSignature(#[from] VerifyError),
    #[error("genesis must be a live version 1, self-signed by an active admin it lists")]
    BadGenesis,
    #[error("the signer has no entry in the held version")]
    UnknownSigner(PublicKey),
    #[error("the signer was not an active admin in the held version")]
    SignerNotActiveAdmin(PublicKey),
    #[error("the candidate is older than the held version")]
    Stale { held: u64, candidate: u64 },
    #[error("same version as held, different bytes")]
    Conflict { version: u64 },
    #[error("the workspace was deleted; no version of it is accepted")]
    Deleted,
}

/// Runs the section 8.1 rule of `held` against `candidate`.
///
/// With no held list, only genesis passes: version 1, live, verifying, signed by
/// a key the list itself carries as an active admin. With a held list, the candidate
/// must verify, name this workspace, and be signed by a key that is active and admin
/// in the held version. A higher version passes even when versions in between were
/// never seen; the rule judges the signer by the held version, so skipping is
/// allowed.
///
/// A deleted candidate goes through the same check. Deletion is a final version,
/// not a special path. Once a deleted version is held, nothing further is accepted:
/// an older version would resurrect the workspace and a higher version would move
/// past its end, so both are refused.
///
/// Equal version with different bytes is a conflict and is refused. The reasoning sits
/// in [`Conflict handling`](Reject::Conflict), documented on the variant below through
/// the module docs here: two same-version lists signed by different admins are a fork,
/// and no device can order them. Accepting either one silently splits the workspace:
/// devices that saw variant A and devices that saw variant B each hold a valid chain
/// and reject each other's next version. Refusing keeps one chain. The refused variant
/// can still win by being re-signed as a higher version by an admin in the held
/// version, which gives the fork a place in the order instead of a place beside it.
pub fn decide(
    held: Option<&SignedTrustList>,
    candidate: &SignedTrustList,
) -> Result<Decision, Reject> {
    candidate.verify()?;
    let Some(held) = held else {
        return decide_genesis(candidate);
    };
    if candidate.list().workspace_id != held.list().workspace_id {
        return Err(Reject::WorkspaceMismatch);
    }
    if held.list().status == WorkspaceStatus::Deleted {
        if candidate.encode() == held.encode() {
            return Ok(Decision::Duplicate);
        }
        return Err(Reject::Deleted);
    }
    match candidate.list().version.cmp(&held.list().version) {
        std::cmp::Ordering::Less => Err(Reject::Stale {
            held: held.list().version,
            candidate: candidate.list().version,
        }),
        std::cmp::Ordering::Equal => {
            if candidate.encode() == held.encode() {
                Ok(Decision::Duplicate)
            } else {
                Err(Reject::Conflict {
                    version: held.list().version,
                })
            }
        }
        std::cmp::Ordering::Greater => {
            let signer = candidate.signer();
            let entry = held.list().entry(signer);
            match entry {
                None => Err(Reject::UnknownSigner(*signer)),
                Some(_) if !is_active_admin(held.list(), signer) => {
                    Err(Reject::SignerNotActiveAdmin(*signer))
                }
                Some(_) => Ok(Decision::Accept),
            }
        }
    }
}

fn decide_genesis(candidate: &SignedTrustList) -> Result<Decision, Reject> {
    let ok = candidate.list().version == 1
        && candidate.list().status == WorkspaceStatus::Live
        && is_active_admin(candidate.list(), candidate.signer());
    if ok {
        Ok(Decision::Accept)
    } else {
        Err(Reject::BadGenesis)
    }
}

#[cfg(test)]
mod tests {
    use iroh::SecretKey;

    use super::*;
    use crate::trust::{Entry, Role, Status, WorkspaceId, WorkspaceStatus};

    struct Fixture {
        admin_a: SecretKey,
        admin_b: SecretKey,
        standard: SecretKey,
        outsider: SecretKey,
        workspace: WorkspaceId,
    }

    impl Fixture {
        fn new() -> Self {
            Self {
                admin_a: SecretKey::generate(),
                admin_b: SecretKey::generate(),
                standard: SecretKey::generate(),
                outsider: SecretKey::generate(),
                workspace: WorkspaceId::generate(),
            }
        }

        fn entry(&self, key: &SecretKey, role: Role, status: Status) -> Entry {
            Entry {
                device_key: key.public(),
                display_name: "device".to_owned(),
                role,
                account_id: None,
                status,
            }
        }

        fn held_v1(&self) -> SignedTrustList {
            TrustList {
                workspace_id: self.workspace,
                name: "home".to_owned(),
                version: 1,
                status: WorkspaceStatus::Live,
                entries: vec![
                    self.entry(&self.admin_a, Role::Admin, Status::Active),
                    self.entry(&self.admin_b, Role::Admin, Status::Active),
                    self.entry(&self.standard, Role::Standard, Status::Active),
                ],
            }
            .sign(&self.admin_a)
        }

        fn list(&self, version: u64, entries: Vec<Entry>) -> TrustList {
            self.list_status(version, entries, WorkspaceStatus::Live)
        }

        fn list_status(
            &self,
            version: u64,
            entries: Vec<Entry>,
            status: WorkspaceStatus,
        ) -> TrustList {
            TrustList {
                workspace_id: self.workspace,
                name: "home".to_owned(),
                version,
                status,
                entries,
            }
        }
    }

    /// One row per rule in the acceptance criterion.
    #[test]
    fn acceptance_table() {
        let f = Fixture::new();
        let held = f.held_v1();

        let live = |signer: &SecretKey, version: u64| {
            f.list(
                version,
                vec![
                    f.entry(&f.admin_a, Role::Admin, Status::Active),
                    f.entry(&f.admin_b, Role::Admin, Status::Active),
                    f.entry(&f.standard, Role::Standard, Status::Active),
                ],
            )
            .sign(signer)
        };

        struct Row {
            name: &'static str,
            candidate: SignedTrustList,
            held_override: Option<SignedTrustList>,
            genesis: bool,
            want: Result<Decision, Reject>,
        }

        // A version that revokes B, and a later version B signs anyway. The rule
        // judges B by the held version, so B passes before the revocation is
        // seen and fails after.
        let b_revoked = f.list(
            2,
            vec![
                f.entry(&f.admin_a, Role::Admin, Status::Active),
                f.entry(&f.admin_b, Role::Admin, Status::Revoked),
                f.entry(&f.standard, Role::Standard, Status::Active),
            ],
        );
        let b_signs_after_revocation = f.list(
            3,
            vec![
                f.entry(&f.admin_a, Role::Admin, Status::Active),
                f.entry(&f.admin_b, Role::Admin, Status::Active),
                f.entry(&f.standard, Role::Standard, Status::Active),
            ],
        );

        let rows = vec![
            Row {
                name: "admin signs next version",
                candidate: live(&f.admin_a, 2),
                held_override: None,
                genesis: false,
                want: Ok(Decision::Accept),
            },
            Row {
                name: "revoked admin is refused",
                candidate: b_signs_after_revocation.clone().sign(&f.admin_b),
                held_override: Some(b_revoked.clone().sign(&f.admin_a)),
                genesis: false,
                want: Err(Reject::SignerNotActiveAdmin(f.admin_b.public())),
            },
            Row {
                name: "active standard device is refused",
                candidate: live(&f.standard, 2),
                held_override: None,
                genesis: false,
                want: Err(Reject::SignerNotActiveAdmin(f.standard.public())),
            },
            Row {
                name: "unknown key is refused",
                candidate: live(&f.outsider, 2),
                held_override: None,
                genesis: false,
                want: Err(Reject::UnknownSigner(f.outsider.public())),
            },
            Row {
                name: "admin revoked in an unseen version still passes",
                candidate: b_signs_after_revocation.sign(&f.admin_b),
                held_override: None,
                genesis: false,
                want: Ok(Decision::Accept),
            },
            Row {
                name: "version skipping passes when the signer was admin in held",
                candidate: live(&f.admin_b, 4),
                held_override: None,
                genesis: false,
                want: Ok(Decision::Accept),
            },
            Row {
                name: "older version is stale",
                candidate: live(&f.admin_a, 1),
                held_override: Some(live(&f.admin_a, 2)),
                genesis: false,
                want: Err(Reject::Stale {
                    held: 2,
                    candidate: 1,
                }),
            },
            Row {
                name: "same bytes twice is a duplicate",
                candidate: held.clone(),
                held_override: None,
                genesis: false,
                want: Ok(Decision::Duplicate),
            },
            Row {
                name: "same version with different bytes is a conflict",
                candidate: f
                    .list(
                        1,
                        vec![
                            f.entry(&f.admin_a, Role::Admin, Status::Active),
                            f.entry(&f.admin_b, Role::Admin, Status::Active),
                            f.entry(&f.standard, Role::Standard, Status::Active),
                        ],
                    )
                    .sign(&f.admin_b),
                held_override: None,
                genesis: false,
                want: Err(Reject::Conflict { version: 1 }),
            },
            Row {
                name: "forged signature is refused before anything else",
                candidate: SignedTrustList::from_parts(
                    live(&f.admin_a, 2).list().clone(),
                    f.admin_a.public(),
                    *live(&f.admin_b, 2).signature(),
                ),
                held_override: None,
                genesis: false,
                want: Err(Reject::BadSignature(VerifyError::BadSignature)),
            },
            Row {
                name: "another workspace is refused",
                candidate: TrustList {
                    workspace_id: WorkspaceId::generate(),
                    name: "home".to_owned(),
                    version: 2,
                    status: WorkspaceStatus::Live,
                    entries: vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                }
                .sign(&f.admin_a),
                held_override: None,
                genesis: false,
                want: Err(Reject::WorkspaceMismatch),
            },
            Row {
                name: "a deleted next version passes the same rule",
                candidate: f
                    .list_status(
                        2,
                        vec![
                            f.entry(&f.admin_a, Role::Admin, Status::Active),
                            f.entry(&f.admin_b, Role::Admin, Status::Active),
                            f.entry(&f.standard, Role::Standard, Status::Active),
                        ],
                        WorkspaceStatus::Deleted,
                    )
                    .sign(&f.admin_a),
                held_override: None,
                genesis: false,
                want: Ok(Decision::Accept),
            },
            Row {
                name: "a deleted version held refuses an older replay",
                candidate: held.clone(),
                held_override: Some(
                    f.list_status(
                        2,
                        vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                        WorkspaceStatus::Deleted,
                    )
                    .sign(&f.admin_a),
                ),
                genesis: false,
                want: Err(Reject::Deleted),
            },
            Row {
                name: "a deleted version held refuses a higher version",
                candidate: live(&f.admin_a, 3),
                held_override: Some(
                    f.list_status(
                        2,
                        vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                        WorkspaceStatus::Deleted,
                    )
                    .sign(&f.admin_a),
                ),
                genesis: false,
                want: Err(Reject::Deleted),
            },
            Row {
                name: "genesis accepts its self-signed first version",
                candidate: TrustList {
                    workspace_id: f.workspace,
                    name: "home".to_owned(),
                    version: 1,
                    status: WorkspaceStatus::Live,
                    entries: vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                }
                .sign(&f.admin_a),
                held_override: None,
                genesis: true,
                want: Ok(Decision::Accept),
            },
            Row {
                name: "genesis refuses a deleted first version",
                candidate: TrustList {
                    workspace_id: f.workspace,
                    name: "home".to_owned(),
                    version: 1,
                    status: WorkspaceStatus::Deleted,
                    entries: vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                }
                .sign(&f.admin_a),
                held_override: None,
                genesis: true,
                want: Err(Reject::BadGenesis),
            },
            Row {
                name: "genesis refuses a list its signer is not admin of",
                candidate: TrustList {
                    workspace_id: f.workspace,
                    name: "home".to_owned(),
                    version: 1,
                    status: WorkspaceStatus::Live,
                    entries: vec![f.entry(&f.admin_a, Role::Admin, Status::Active)],
                }
                .sign(&f.outsider),
                held_override: None,
                genesis: true,
                want: Err(Reject::BadGenesis),
            },
        ];

        for row in rows {
            let held_opt: Option<SignedTrustList> = if row.genesis {
                None
            } else {
                Some(row.held_override.unwrap_or_else(|| held.clone()))
            };
            assert_eq!(
                decide(held_opt.as_ref(), &row.candidate),
                row.want,
                "row failed: {}",
                row.name
            );
        }
    }
}
