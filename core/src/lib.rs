//! `rfm-core` — everything a device needs to decide who may touch which files, and to move
//! them once that is decided.
//!
//! The surface arrives issue by issue: identity and the keystore (#6), the trust list
//! (#7–#11), shares and the access rule (#12–#14), the file protocol (#15–#17), the UI API
//! (#18).
//!
//! Two rules this crate is held to, from `docs/adr/`:
//!
//! - The cloud can revoke but never grant. No code path here takes an authorization
//!   decision from the control plane. (0001)
//! - The protocol is written against an abstract bidirectional stream, never against an
//!   `iroh` type, so the transport stays swappable. (0002)

pub mod config;
pub mod fs;
pub mod identity;
pub mod transport;
pub mod trust;

/// The version of the on-wire protocol this build speaks.
///
/// Bumped by #15 when the framing is defined; peers that disagree must not proceed.
pub const PROTOCOL_VERSION: u32 = 0;

/// The ALPN this build negotiates.
pub const ALPN: &[u8] = b"rfm/1";

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn alpn_matches_the_spec() {
        // r3 §19. If this changes, every deployed peer stops talking to every other one,
        // so the constant is tested rather than trusted.
        assert_eq!(ALPN, b"rfm/1");
    }
}
