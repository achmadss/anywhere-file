# The canonical trust list encoding

Version 1. Normative. `core/src/trust/encoding.rs` implements this document; where they
disagree, this document is right and the code is a bug.

A workspace is its trust list (r3 §8.1), and a trust list is only worth what its signature
is worth. A signature is over bytes, so the bytes have to be reproducible: any two devices
holding the same membership must compute the same byte string for it, or each will read the
other's list as forged. That is what "canonical" means here, and it is the whole reason this
document exists.

## Where the encoding is used

In three places, and it is the same bytes in all three.

- The message that gets signed and verified.
- The file on disk, in `<config>/trust/<workspace-id>.tl`.
- The bytes sent to a peer during propagation (#10) and pairing (#17).

There is no separate storage or wire format. A signed structure that is re-serialized before
it is stored opens a gap between what was verified and what was kept, and that gap is
exactly where a bug becomes a trust failure.

## Conventions

All integers are unsigned and big-endian. All strings are UTF-8, length-prefixed with a
`u32`, and not null-terminated. There is no padding and no alignment anywhere.

## Layout

A trust list file is the body followed by the signature.

```text
body       as below
signature  64 bytes, Ed25519 over the whole of body
```

The body:

| Field | Bytes | Notes |
|---|---|---|
| magic | 15 | `rfm-trust-list\0` |
| encoding version | 1 | `1` for this document |
| signer | 32 | Ed25519 public key of the device that signed |
| workspace id | 16 | random, minted by the creating agent |
| name length | 4 | `u32`, at most 4096 |
| name | *n* | UTF-8, a label for humans only |
| list version | 8 | `u64`, monotonic |
| entry count | 4 | `u32`, at most 4096 |
| entries | | `count` of them, in the order defined below |

Each entry:

| Field | Bytes | Notes |
|---|---|---|
| device key | 32 | Ed25519 public key, also the iroh endpoint id |
| display name length | 4 | `u32`, at most 4096 |
| display name | *n* | UTF-8 |
| role | 1 | `0` standard, `1` admin |
| account id present | 1 | `0` absent, `1` present |
| account id length | 4 | `u32`, only when present |
| account id | *n* | UTF-8, only when present |
| status | 1 | `0` active, `1` revoked |

## Entry order

Entries are sorted ascending by the 32 raw bytes of `device_key`, compared
lexicographically. This is the rule that makes the encoding canonical: entries reach a trust
list in whatever order they were added or iterated out of a map, and without a defined order
two devices agreeing on the membership would still compute different signatures over it.

A decoder rejects entries that are not in ascending order. Bytes in another order did not
come from a conforming encoder, and accepting them would mean two byte strings for one
membership when only one of them can carry a signature that verifies.

A device key appearing twice is also rejected. It is not a conflict to be merged later; it
is an ambiguity about what a device may do.

## Why the signer is inside the signed bytes

Ed25519 does not bind a signature to a public key by construction, so a message that does
not name its signer can in principle be presented under a second key. Naming the signer
inside the signed body removes the question for 32 bytes.

It also gives #8 what it needs. The §8.1 succession rule is "signed by a key that was
`active` and `admin` in the version currently held", and that check needs to know which key
signed before it can look it up.

## Version 1 and what would make a version 2

The encoding version is the second field, before anything variable-length, so a decoder can
refuse an unknown version before it has parsed anything it might misread. Version 1 decoders
refuse anything that is not `1` rather than skipping fields they do not recognize: a trust
list understood approximately is worse than one not read at all.

Any change to field order, field widths, the entry sort key, or the meaning of a
discriminant is a new version. Adding a field is a new version too, because the signature
covers the exact bytes and an old encoder would produce a different string for the same
membership.

## Limits

A string is at most 4096 bytes and a list at most 4096 entries. These are bounds on what a
decoder will allocate for input it has not yet authenticated, not product limits. Trust list
bytes arrive from the network before anything has verified them (#10), so every length read
from them is checked before it is used.
