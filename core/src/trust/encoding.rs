//! The canonical encoding of a trust list.
//!
//! Specified in [`docs/trust-list-encoding.md`](../../../docs/trust-list-encoding.md). Read
//! that first; this file is the implementation of it and the doc is the normative text.
//!
//! Two things about it are load-bearing and neither is obvious from the code alone.
//!
//! It is hand-written rather than a serialization framework because the bytes are what gets
//! signed. A framework's output is stable only as long as nobody reorders a struct field,
//! renames one, or upgrades the framework, and none of those are things a compiler will
//! stop. Here the byte order is written out in one function that has to be edited on
//! purpose, and `ENCODING_VERSION` is the first thing in the output.
//!
//! It is also the on-disk and on-wire form, not a second encoding used only for signing. A
//! signed form that is re-serialized before it is stored gives an attacker a gap between
//! what was verified and what is kept.

use std::collections::BTreeSet;

use iroh::{PublicKey, Signature};

use super::{Entry, Role, SignedTrustList, Status, TrustList, WorkspaceId};

/// Domain separation. A device signs trust lists here and will sign other things later
/// (#11's recovery bundle, #17's pairing); no signature over one may ever be replayed as a
/// signature over another.
const MAGIC: &[u8; 15] = b"rfm-trust-list\0";

/// The version of this encoding, not of a trust list. A decoder that does not know a
/// version refuses the bytes rather than guessing at them.
pub const ENCODING_VERSION: u8 = 1;

const SIGNATURE_LEN: usize = 64;

/// Ceiling on a length-prefixed string, so a corrupt or hostile length cannot make the
/// decoder allocate. No display name or account id is anywhere near this.
const MAX_STRING_LEN: u32 = 4096;

/// Ceiling on the entry count, for the same reason. A workspace is one person's devices.
const MAX_ENTRIES: u32 = 4096;

pub(super) fn encode(list: &TrustList, signer: &PublicKey) -> Vec<u8> {
    let mut out = Vec::new();
    out.extend_from_slice(MAGIC);
    out.push(ENCODING_VERSION);
    out.extend_from_slice(signer.as_bytes());
    out.extend_from_slice(list.workspace_id.as_bytes());
    put_str(&mut out, &list.name);
    out.extend_from_slice(&list.version.to_be_bytes());

    // Sorting here is what makes the encoding canonical. Entries reach a `TrustList` in
    // whatever order they were added or iterated out of a map, and two devices that
    // disagree about that order would compute different signatures over the same
    // membership and each conclude the other had tampered.
    let mut entries: Vec<&Entry> = list.entries.iter().collect();
    entries.sort_unstable_by_key(|e| *e.device_key.as_bytes());

    out.extend_from_slice(&(entries.len() as u32).to_be_bytes());
    for entry in entries {
        out.extend_from_slice(entry.device_key.as_bytes());
        put_str(&mut out, &entry.display_name);
        out.push(match entry.role {
            Role::Standard => 0,
            Role::Admin => 1,
        });
        match &entry.account_id {
            None => out.push(0),
            Some(id) => {
                out.push(1);
                put_str(&mut out, id);
            }
        }
        out.push(match entry.status {
            Status::Active => 0,
            Status::Revoked => 1,
        });
    }
    out
}

fn put_str(out: &mut Vec<u8>, s: &str) {
    out.extend_from_slice(&(s.len() as u32).to_be_bytes());
    out.extend_from_slice(s.as_bytes());
}

pub(super) fn decode(bytes: &[u8]) -> Result<SignedTrustList, DecodeError> {
    let mut r = Reader { bytes, at: 0 };

    if r.take(MAGIC.len())? != MAGIC {
        return Err(DecodeError::NotATrustList);
    }
    let version = r.u8()?;
    if version != ENCODING_VERSION {
        return Err(DecodeError::UnknownEncodingVersion(version));
    }

    let signer = r.public_key()?;
    let workspace_id =
        WorkspaceId::from_bytes(r.take(16)?.try_into().expect("take(16) returned 16 bytes"));
    let name = r.string()?;
    let list_version = u64::from_be_bytes(r.take(8)?.try_into().expect("take(8) returned 8"));

    let count = r.u32()?;
    if count > MAX_ENTRIES {
        return Err(DecodeError::TooManyEntries(count));
    }
    let mut entries = Vec::with_capacity(count as usize);
    let mut seen = BTreeSet::new();
    let mut previous: Option<[u8; 32]> = None;
    for _ in 0..count {
        let device_key = r.public_key()?;
        let key_bytes = *device_key.as_bytes();

        // Two entries for one key is not a merge conflict to resolve later; it is an
        // ambiguity about what a device is allowed to do, so the list is refused.
        if !seen.insert(key_bytes) {
            return Err(DecodeError::DuplicateDeviceKey);
        }
        // The encoder sorts, so bytes that arrive unsorted did not come from it. Accepting
        // them would mean two byte strings for one membership, and only one of them can
        // carry a valid signature.
        if previous.is_some_and(|p| p >= key_bytes) {
            return Err(DecodeError::EntriesNotSorted);
        }
        previous = Some(key_bytes);

        let display_name = r.string()?;
        let role = match r.u8()? {
            0 => Role::Standard,
            1 => Role::Admin,
            other => return Err(DecodeError::UnknownRole(other)),
        };
        let account_id = match r.u8()? {
            0 => None,
            1 => Some(r.string()?),
            other => return Err(DecodeError::UnknownOptionTag(other)),
        };
        let status = match r.u8()? {
            0 => Status::Active,
            1 => Status::Revoked,
            other => return Err(DecodeError::UnknownStatus(other)),
        };
        entries.push(Entry {
            device_key,
            display_name,
            role,
            account_id,
            status,
        });
    }

    let signature: [u8; SIGNATURE_LEN] = r
        .take(SIGNATURE_LEN)?
        .try_into()
        .expect("take(64) returned 64 bytes");
    if r.at != r.bytes.len() {
        return Err(DecodeError::TrailingBytes(r.bytes.len() - r.at));
    }

    Ok(SignedTrustList::from_parts(
        TrustList {
            workspace_id,
            name,
            version: list_version,
            entries,
        },
        signer,
        Signature::from_bytes(&signature),
    ))
}

struct Reader<'a> {
    bytes: &'a [u8],
    at: usize,
}

impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], DecodeError> {
        let end = self.at.checked_add(n).ok_or(DecodeError::Truncated)?;
        let slice = self.bytes.get(self.at..end).ok_or(DecodeError::Truncated)?;
        self.at = end;
        Ok(slice)
    }

    fn u8(&mut self) -> Result<u8, DecodeError> {
        Ok(self.take(1)?[0])
    }

    fn u32(&mut self) -> Result<u32, DecodeError> {
        Ok(u32::from_be_bytes(
            self.take(4)?.try_into().expect("take(4) returned 4 bytes"),
        ))
    }

    fn public_key(&mut self) -> Result<PublicKey, DecodeError> {
        let bytes: [u8; 32] = self
            .take(32)?
            .try_into()
            .expect("take(32) returned 32 bytes");
        PublicKey::from_bytes(&bytes).map_err(|_| DecodeError::NotAPublicKey)
    }

    fn string(&mut self) -> Result<String, DecodeError> {
        let len = self.u32()?;
        if len > MAX_STRING_LEN {
            return Err(DecodeError::StringTooLong(len));
        }
        String::from_utf8(self.take(len as usize)?.to_vec()).map_err(|_| DecodeError::NotUtf8)
    }
}

/// Why some bytes are not a trust list.
///
/// Every variant is a refusal. There is no lenient path: a trust list that cannot be
/// decoded exactly is not a trust list, because the thing that was signed was the exact
/// bytes.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum DecodeError {
    #[error("the bytes do not begin with the trust list magic")]
    NotATrustList,
    #[error("encoding version {0} is not one this build knows")]
    UnknownEncodingVersion(u8),
    #[error("the bytes end in the middle of a field")]
    Truncated,
    #[error("{0} bytes after the signature")]
    TrailingBytes(usize),
    #[error("a 32-byte field is not a valid Ed25519 public key")]
    NotAPublicKey,
    #[error("a length-prefixed string is not UTF-8")]
    NotUtf8,
    #[error("a length-prefixed string claims {0} bytes")]
    StringTooLong(u32),
    #[error("the list claims {0} entries")]
    TooManyEntries(u32),
    #[error("two entries carry the same device key")]
    DuplicateDeviceKey,
    #[error("entries are not in ascending device key order, so these are not canonical bytes")]
    EntriesNotSorted,
    #[error("role byte {0} is not one this build knows")]
    UnknownRole(u8),
    #[error("status byte {0} is not one this build knows")]
    UnknownStatus(u8),
    #[error("optional-field tag {0} is neither absent (0) nor present (1)")]
    UnknownOptionTag(u8),
}

#[cfg(test)]
mod tests {
    use iroh::SecretKey;

    use super::{super::VerifyError, *};

    fn entry(key: &SecretKey, name: &str) -> Entry {
        Entry {
            device_key: key.public(),
            display_name: name.to_owned(),
            role: Role::Standard,
            account_id: None,
            status: Status::Active,
        }
    }

    fn list(entries: Vec<Entry>) -> TrustList {
        TrustList {
            workspace_id: WorkspaceId::from_bytes([7; 16]),
            name: "kitchen table".to_owned(),
            version: 3,
            entries,
        }
    }

    /// The acceptance criterion from #7: the encoding does not depend on the order entries
    /// were put into the list. Every permutation of the same membership is one byte string.
    #[test]
    fn entry_order_does_not_change_the_bytes() {
        let keys: Vec<SecretKey> = (0..4).map(|_| SecretKey::generate()).collect();
        let entries: Vec<Entry> = keys
            .iter()
            .enumerate()
            .map(|(i, k)| entry(k, &format!("device {i}")))
            .collect();
        let signer = SecretKey::generate().public();

        let expected = encode(&list(entries.clone()), &signer);

        // All 24 permutations of four entries, generated by rotating each suffix.
        let mut permutation = entries;
        for _ in 0..24 {
            permutation.rotate_left(1);
            permutation[1..].rotate_left(1);
            permutation[2..].rotate_left(1);
            assert_eq!(
                encode(&list(permutation.clone()), &signer),
                expected,
                "a permutation of the same entries encoded differently"
            );
        }
    }

    #[test]
    fn round_trips_through_decode() {
        let signer = SecretKey::generate();
        let a = SecretKey::generate();
        let b = SecretKey::generate();
        let original = TrustList {
            workspace_id: WorkspaceId::generate(),
            name: "hüttenschlüssel 🔑".to_owned(),
            version: u64::MAX,
            entries: vec![
                Entry {
                    device_key: a.public(),
                    display_name: "laptop".to_owned(),
                    role: Role::Admin,
                    account_id: Some("acct_01".to_owned()),
                    status: Status::Active,
                },
                Entry {
                    device_key: b.public(),
                    display_name: String::new(),
                    role: Role::Standard,
                    account_id: None,
                    status: Status::Revoked,
                },
            ],
        };

        let signed = original.clone().sign(&signer);
        let decoded = SignedTrustList::decode(&signed.encode()).expect("decode");

        assert_eq!(decoded.signer(), &signer.public());
        assert_eq!(decoded.list().workspace_id, original.workspace_id);
        assert_eq!(decoded.list().name, original.name);
        assert_eq!(decoded.list().version, original.version);
        assert_eq!(decoded.list().entries.len(), 2);
        decoded.verify().expect("verify");

        // Decoding and re-encoding is the identity, which is what lets the stored bytes be
        // the verified bytes.
        assert_eq!(decoded.encode(), signed.encode());
    }

    /// Every byte of the body is covered by the signature. Flipping any one of them has to
    /// break verification; a field the signature does not reach is a field an attacker
    /// gets to choose.
    #[test]
    fn flipping_any_body_byte_breaks_verification() {
        let signer = SecretKey::generate();
        let signed = list(vec![entry(&SecretKey::generate(), "laptop")]).sign(&signer);
        let bytes = signed.encode();
        let body_len = bytes.len() - SIGNATURE_LEN;

        for i in 0..body_len {
            let mut tampered = bytes.clone();
            tampered[i] ^= 0x01;
            // A flip may break the parse (a bad magic, a bad key, a length out of range)
            // or survive it and break the signature. Either is a refusal; what must never
            // happen is a decode that then verifies.
            if let Ok(decoded) = SignedTrustList::decode(&tampered) {
                assert!(
                    decoded.verify().is_err(),
                    "flipping byte {i} produced a list that still verifies"
                );
            }
        }
    }

    #[test]
    fn a_signature_from_another_key_does_not_verify() {
        let signed =
            list(vec![entry(&SecretKey::generate(), "laptop")]).sign(&SecretKey::generate());
        let impostor = SecretKey::generate();

        // Claim a different signer while keeping the original signature.
        let forged = SignedTrustList::from_parts(
            signed.list().clone(),
            impostor.public(),
            *signed.signature(),
        );
        assert_eq!(forged.verify(), Err(VerifyError::BadSignature));
    }

    #[test]
    fn unsorted_entries_are_refused() {
        let a = SecretKey::generate();
        let b = SecretKey::generate();
        let (low, high) = if a.public().as_bytes() < b.public().as_bytes() {
            (a, b)
        } else {
            (b, a)
        };
        let signer = SecretKey::generate();
        // Equal-length display names, so the two encoded entries are the same size and
        // the swap below is a straight window exchange.
        let signed = list(vec![entry(&low, "aaa"), entry(&high, "bbb")]).sign(&signer);
        let bytes = signed.encode();

        // Swap the two encoded entries. They are the same length, so this is a byte swap
        // of two equal-sized windows and the rest of the frame is untouched.
        let header = MAGIC.len() + 1 + 32 + 16 + 4 + "kitchen table".len() + 8 + 4;
        let entry_len = (bytes.len() - SIGNATURE_LEN - header) / 2;
        let mut swapped = bytes.clone();
        swapped[header..header + entry_len]
            .copy_from_slice(&bytes[header + entry_len..header + 2 * entry_len]);
        swapped[header + entry_len..header + 2 * entry_len]
            .copy_from_slice(&bytes[header..header + entry_len]);

        assert_eq!(
            SignedTrustList::decode(&swapped),
            Err(DecodeError::EntriesNotSorted)
        );
    }

    #[test]
    fn a_duplicate_device_key_is_refused() {
        let key = SecretKey::generate();
        let signer = SecretKey::generate();
        // Two entries for one key, so the sort leaves them adjacent and equal.
        let signed = list(vec![entry(&key, "one"), entry(&key, "two")]).sign(&signer);
        assert_eq!(
            SignedTrustList::decode(&signed.encode()),
            Err(DecodeError::DuplicateDeviceKey)
        );
    }

    #[test]
    fn truncation_and_trailing_bytes_are_refused() {
        let signed =
            list(vec![entry(&SecretKey::generate(), "laptop")]).sign(&SecretKey::generate());
        let bytes = signed.encode();

        assert_eq!(SignedTrustList::decode(&[]), Err(DecodeError::Truncated));
        assert_eq!(
            SignedTrustList::decode(&[0xff; 64]),
            Err(DecodeError::NotATrustList)
        );
        assert_eq!(
            SignedTrustList::decode(&bytes[..bytes.len() - 1]),
            Err(DecodeError::Truncated)
        );

        let mut extra = bytes.clone();
        extra.push(0);
        assert_eq!(
            SignedTrustList::decode(&extra),
            Err(DecodeError::TrailingBytes(1))
        );
    }

    #[test]
    fn an_unknown_encoding_version_is_refused_before_anything_is_parsed() {
        let signed =
            list(vec![entry(&SecretKey::generate(), "laptop")]).sign(&SecretKey::generate());
        let mut bytes = signed.encode();
        bytes[MAGIC.len()] = ENCODING_VERSION + 1;
        assert_eq!(
            SignedTrustList::decode(&bytes),
            Err(DecodeError::UnknownEncodingVersion(ENCODING_VERSION + 1))
        );
    }

    /// A length field arrives from the network before anything has authenticated it, so a
    /// hostile one must not become an allocation.
    #[test]
    fn an_absurd_string_length_is_refused_rather_than_allocated() {
        let signed =
            list(vec![entry(&SecretKey::generate(), "laptop")]).sign(&SecretKey::generate());
        let mut bytes = signed.encode();
        let name_len_at = MAGIC.len() + 1 + 32 + 16;
        bytes[name_len_at..name_len_at + 4].copy_from_slice(&u32::MAX.to_be_bytes());
        assert_eq!(
            SignedTrustList::decode(&bytes),
            Err(DecodeError::StringTooLong(u32::MAX))
        );
    }
}
