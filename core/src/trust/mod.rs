//! The trust list: the workspace, and the only root of authority in the system.
//!
//! r3 §8.1. A workspace *is* its trust list. Every question in §15's authority matrix
//! reduces to "what does the list I hold say", so the bytes that get signed and the rules
//! for verifying them are the foundation everything else stands on.
//!
//! This module owns the types, the canonical encoding, signing, verification and
//! persistence. It deliberately does **not** decide whether a list should be accepted:
//! that is the §8.1 succession rule, and it is #8's, in [`accept`](super::trust) terms
//! that need the version currently held. [`SignedTrustList::verify`] answers only "was
//! this signed by the key it claims", which is the question #8 asks first.

mod encoding;
mod store;

use iroh::{PublicKey, SecretKey, Signature};

pub use self::{
    encoding::{DecodeError, ENCODING_VERSION},
    store::{StoreError, TrustStore},
};

/// A workspace identifier: random 128 bits, minted by the creating agent.
///
/// Random rather than derived, so that no property of the creating device leaks into it
/// and two workspaces created by one device are unlinkable to anyone watching DNS-SD (#9),
/// which puts this value in a TXT record in the clear.
#[derive(
    Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, serde::Serialize, serde::Deserialize,
)]
#[serde(into = "String", try_from = "String")]
pub struct WorkspaceId([u8; 16]);

impl WorkspaceId {
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

    /// Reconstructs an identifier from its raw bytes.
    pub fn from_bytes(bytes: [u8; 16]) -> Self {
        Self(bytes)
    }
}

/// Parses the hex form, which is what #9 reads out of a `ws=` TXT record.
impl std::str::FromStr for WorkspaceId {
    type Err = crate::share::IdParseError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        crate::share::parse_hex16(s).map(Self)
    }
}

impl From<WorkspaceId> for String {
    fn from(id: WorkspaceId) -> Self {
        id.to_string()
    }
}

impl TryFrom<String> for WorkspaceId {
    type Error = crate::share::IdParseError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        s.parse()
    }
}

/// Lower-case hex, which is also the file name in the store and the `ws=` TXT value in #9.
impl std::fmt::Display for WorkspaceId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        for byte in self.0 {
            write!(f, "{byte:02x}")?;
        }
        Ok(())
    }
}

/// What a device may do in a workspace (r3 §15).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Role {
    /// May browse and transfer within its grants. Cannot admit anyone.
    Standard,
    /// May sign a new version of the trust list, and so may admit, revoke and promote.
    Admin,
}

/// Whether an entry still counts (r3 §8.5).
///
/// Revoked entries are kept rather than deleted: a device has to be able to tell "this key
/// was thrown out" from "I have never heard of this key", and the difference decides
/// whether a stale list is safe to accept.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Status {
    Active,
    Revoked,
}

/// One device's membership of one workspace.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    /// The device's Ed25519 public key, which is also its iroh endpoint id.
    pub device_key: PublicKey,
    /// A label for humans. Carries no authority.
    pub display_name: String,
    pub role: Role,
    /// The cloud account this device was admitted under, if it was admitted remotely
    /// (r3 §8.3). Locally paired devices have none (§8.2), which is why it is optional and
    /// why nothing may require it.
    pub account_id: Option<String>,
    pub status: Status,
}

/// The signed part of a trust list: everything the signature covers.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TrustList {
    pub workspace_id: WorkspaceId,
    /// A label for humans (r3 D3). Not a hostname and not an identifier.
    pub name: String,
    /// Monotonic. The successor rule in §8.1 is stated in terms of it.
    pub version: u64,
    /// Sorted by `device_key` in the canonical encoding, whatever order they arrive in.
    pub entries: Vec<Entry>,
}

impl TrustList {
    /// Signs this list with `signer`, producing the form that is persisted and sent.
    ///
    /// Whether `signer` is *allowed* to sign is #8's question, not this one.
    pub fn sign(self, signer: &SecretKey) -> SignedTrustList {
        let signer_key = signer.public();
        let body = encoding::encode(&self, &signer_key);
        let signature = signer.sign(&body);
        SignedTrustList {
            list: self,
            signer: signer_key,
            signature,
        }
    }

    /// The entry for `device_key`, if the list has one.
    pub fn entry(&self, device_key: &PublicKey) -> Option<&Entry> {
        self.entries.iter().find(|e| &e.device_key == device_key)
    }
}

/// A trust list together with the key that signed it and the signature.
///
/// The signer is inside the signed bytes, not merely alongside them. Ed25519 signatures are
/// not bound to a public key by construction, so a message that does not name its signer can
/// in principle be presented under a second key. Naming it inside removes the question, and
/// it costs 32 bytes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SignedTrustList {
    list: TrustList,
    signer: PublicKey,
    signature: Signature,
}

impl SignedTrustList {
    /// The list. Reachable without verifying, because #8 has to read the version and the
    /// signer before it can decide whether verification is even worth doing.
    pub fn list(&self) -> &TrustList {
        &self.list
    }

    /// The key that signed. Whether it *may* sign is #8's succession rule.
    pub fn signer(&self) -> &PublicKey {
        &self.signer
    }

    pub fn signature(&self) -> &Signature {
        &self.signature
    }

    /// Checks the signature against the canonical bytes.
    ///
    /// This is necessary and not sufficient. A list that verifies here was signed by the
    /// key it names; #8 decides whether that key was `active` and `admin` in the version
    /// this device already holds.
    pub fn verify(&self) -> Result<(), VerifyError> {
        let body = encoding::encode(&self.list, &self.signer);
        self.signer
            .verify(&body, &self.signature)
            .map_err(|_| VerifyError::BadSignature)
    }

    /// The canonical wire and on-disk form: the signed bytes, then the 64-byte signature.
    pub fn encode(&self) -> Vec<u8> {
        let mut out = encoding::encode(&self.list, &self.signer);
        out.extend_from_slice(&self.signature.to_bytes());
        out
    }

    /// Parses [`SignedTrustList::encode`]. Does not verify; call [`Self::verify`].
    pub fn decode(bytes: &[u8]) -> Result<Self, DecodeError> {
        encoding::decode(bytes)
    }

    /// Reassembles a decoded list. Used by [`Self::decode`] and by tests that need to
    /// build a list whose signature does not match it.
    pub(crate) fn from_parts(list: TrustList, signer: PublicKey, signature: Signature) -> Self {
        Self {
            list,
            signer,
            signature,
        }
    }
}

/// Why a signature did not check out.
#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
pub enum VerifyError {
    /// The signature is not a valid signature by the named signer over these bytes. Either
    /// the list was altered after signing or the signer is not who the list says.
    #[error("the trust list signature does not verify against the key it names")]
    BadSignature,
}
