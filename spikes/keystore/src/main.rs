//! Spike #2: can the Ed25519 device key live in the OS keystore, and does a key that came
//! back out of the keystore actually authenticate an iroh connection?
//!
//! Two storage shapes are exercised side by side:
//!
//!   direct   the 32-byte Ed25519 seed is the keystore item.
//!   wrapped  a 32-byte wrapping key is the keystore item, and the seed is sealed with
//!            XChaCha20-Poly1305 into a file next to it. This is the shape that survives
//!            a machine with no keystore at all, where the wrapping key comes from
//!            somewhere else.
//!
//! Subcommands, run as separate processes so "restart" means a real restart:
//!   store     generate a key, write both shapes, print the public key
//!   load      read both shapes back, check they agree, run an iroh handshake with the
//!             reloaded key and check the peer sees that public key
//!   wipe      delete both keystore items
//!
//! Throwaway. Not a workspace member. `cargo run --release -- <cmd>` from this directory.

use std::path::PathBuf;

use chacha20poly1305::{
    XChaCha20Poly1305, XNonce,
    aead::{Aead, AeadCore, KeyInit, OsRng},
};
use data_encoding::HEXLOWER;
use iroh::{Endpoint, RelayMode, SecretKey, endpoint::presets};

type Res<T = ()> = Result<T, Box<dyn std::error::Error + Send + Sync>>;

const SERVICE: &str = "dev.anywherefile.spike-keystore";
const ACCOUNT_DIRECT: &str = "device-key-seed";
const ACCOUNT_WRAP: &str = "device-key-wrapping-key";
const ALPN: &[u8] = b"spike/keystore/1";

fn sealed_path() -> PathBuf {
    std::env::temp_dir().join("spike-keystore-device.key.sealed")
}

fn entry(account: &str) -> Res<keyring::Entry> {
    Ok(keyring::Entry::new(SERVICE, account)?)
}

/// Seal 32 bytes under a fresh wrapping key. Returns (wrapping key, nonce || ciphertext).
fn seal(seed: &[u8; 32]) -> Res<([u8; 32], Vec<u8>)> {
    let key = XChaCha20Poly1305::generate_key(&mut OsRng);
    let cipher = XChaCha20Poly1305::new(&key);
    let nonce = XChaCha20Poly1305::generate_nonce(&mut OsRng);
    let mut out = nonce.to_vec();
    out.extend_from_slice(
        &cipher
            .encrypt(&nonce, seed.as_slice())
            .map_err(|e| e.to_string())?,
    );
    Ok((key.into(), out))
}

fn unseal(key: &[u8; 32], blob: &[u8]) -> Res<[u8; 32]> {
    let (nonce, ct) = blob.split_at(24);
    let cipher = XChaCha20Poly1305::new(key.into());
    let plain = cipher
        .decrypt(XNonce::from_slice(nonce), ct)
        .map_err(|e| e.to_string())?;
    Ok(plain.as_slice().try_into()?)
}

fn store() -> Res {
    let key = SecretKey::generate();
    let seed = key.to_bytes();
    println!("generated       public key = {}", key.public());

    entry(ACCOUNT_DIRECT)?.set_secret(&seed)?;
    println!(
        "direct          wrote {} bytes to keystore {SERVICE}/{ACCOUNT_DIRECT}",
        seed.len()
    );

    let (wrap_key, blob) = seal(&seed)?;
    entry(ACCOUNT_WRAP)?.set_secret(&wrap_key)?;
    std::fs::write(sealed_path(), &blob)?;
    println!(
        "wrapped         wrapping key in keystore {SERVICE}/{ACCOUNT_WRAP}, {} byte sealed file at {}",
        blob.len(),
        sealed_path().display()
    );
    println!("\nexpected public key = {}", key.public());
    Ok(())
}

fn load_direct() -> Res<SecretKey> {
    let seed: [u8; 32] = entry(ACCOUNT_DIRECT)?.get_secret()?.as_slice().try_into()?;
    Ok(SecretKey::from_bytes(&seed))
}

fn load_wrapped() -> Res<SecretKey> {
    let wrap_key: [u8; 32] = entry(ACCOUNT_WRAP)?.get_secret()?.as_slice().try_into()?;
    let blob = std::fs::read(sealed_path())?;
    Ok(SecretKey::from_bytes(&unseal(&wrap_key, &blob)?))
}

/// Bind two endpoints in this process, dial one from the other with `key` on the dialling
/// side, and report what the accepting side saw. A raw-public-key TLS handshake only
/// completes if the dialler holds the private half, so `remote_id` on the accept side is
/// proof the reloaded bytes are the live signing key.
async fn handshake(key: SecretKey) -> Res {
    let server = Endpoint::builder(presets::Minimal)
        .relay_mode(RelayMode::Disabled)
        .alpns(vec![ALPN.to_vec()])
        .bind()
        .await?;
    let client = Endpoint::builder(presets::Minimal)
        .relay_mode(RelayMode::Disabled)
        .secret_key(key.clone())
        .bind()
        .await?;

    let expect = key.public();
    let addr = server.addr();
    let accept = tokio::spawn(async move {
        let conn = server.accept().await.ok_or("endpoint closed")?.await?;
        let seen = conn.remote_id();
        let mut recv = conn.accept_uni().await?;
        let msg = recv.read_to_end(64).await?;
        Ok::<_, Box<dyn std::error::Error + Send + Sync>>((seen, msg))
    });

    let conn = client.connect(addr, ALPN).await?;
    let mut send = conn.open_uni().await?;
    send.write_all(b"hello from the keystore").await?;
    send.finish()?;

    let (seen, msg) = accept.await??;
    println!("handshake       peer saw remote_id = {seen}");
    println!(
        "handshake       payload = {:?}",
        String::from_utf8_lossy(&msg)
    );
    assert_eq!(seen, expect, "peer authenticated a different key");
    println!("handshake       OK: matches the reloaded key");

    conn.close(0u32.into(), b"done");
    client.close().await;
    Ok(())
}

fn wipe() -> Res {
    for account in [ACCOUNT_DIRECT, ACCOUNT_WRAP] {
        match entry(account)?.delete_credential() {
            Ok(()) => println!("deleted {SERVICE}/{account}"),
            Err(e) => println!("delete {SERVICE}/{account}: {e}"),
        }
    }
    let _ = std::fs::remove_file(sealed_path());
    Ok(())
}

#[tokio::main]
async fn main() -> Res {
    match std::env::args().nth(1).as_deref() {
        Some("store") => store(),
        Some("wipe") => wipe(),
        Some("load") => {
            let direct = load_direct()?;
            println!("direct          reloaded public key = {}", direct.public());
            let wrapped = load_wrapped()?;
            println!("wrapped         reloaded public key = {}", wrapped.public());
            assert_eq!(direct.public(), wrapped.public(), "the two shapes disagree");
            assert_eq!(
                HEXLOWER.encode(&direct.to_bytes()),
                HEXLOWER.encode(&wrapped.to_bytes()),
                "seeds differ"
            );
            println!("both shapes agree on the seed\n");
            handshake(direct).await
        }
        other => {
            eprintln!("usage: spike-keystore store|load|wipe (got {other:?})");
            std::process::exit(2);
        }
    }
}
