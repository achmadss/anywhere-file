//! An agent configured for our relays never contacts a public one.
//!
//! This runs in CI with no infrastructure. It checks the relay mode the agent
//! is handed (custom map, our URLs only) and it scans the agent, core, cloud
//! and deploy sources for anything that would point at a public relay. The
//! connection-level proof needs a deployed fleet: tests/fleet_transfer.rs.

use std::path::{Path, PathBuf};

use iroh::{RelayMode, RelayUrl};

/// The fleet under test: RFM_RELAY_URLS when set, else the example pair.
fn fleet_urls() -> Vec<RelayUrl> {
    rfm_fleet::relay_urls_from_env().unwrap_or_else(rfm_fleet::example_urls)
}

/// The mode we hand the agent holds our relays and only ours.
#[test]
fn agent_relay_mode_holds_only_our_relays() {
    let urls = fleet_urls();
    assert!(!urls.is_empty(), "fleet needs at least one relay URL");

    let mode = rfm_fleet::relay_mode(&urls);
    let RelayMode::Custom(map) = &mode else {
        panic!("fleet relay mode must be Custom, got {mode:?}");
    };

    let got: Vec<RelayUrl> = map.urls();
    assert_eq!(
        got.len(),
        urls.len(),
        "relay map gained or lost a relay: {got:?}"
    );
    for url in &urls {
        assert!(
            map.contains(url),
            "configured relay missing from the map: {url}"
        );
    }

    rfm_fleet::assert_no_public_relay(&mode);
}

/// iroh's Default and Staging modes both point at n0's public relays. Neither
/// may appear where the agent is configured.
#[test]
fn no_default_or_staging_relay_mode_in_agent_sources() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..");
    let mut hits = Vec::new();
    // The fleet crate is the guard, not the guarded: it names the public
    // relays to deny-list them. Everything else is scanned.
    for dir in ["agent", "core", "cloud", "relay/deploy"] {
        let path = root.join(dir);
        // A scan that finds nothing because the directory moved would pass and
        // guard nothing, so a missing directory is a failure here.
        assert!(path.is_dir(), "scan target is missing: {}", path.display());
        scan_dir(&path, &mut hits);
    }
    assert!(
        hits.is_empty(),
        "public relay reference in agent sources:\n{}",
        hits.join("\n")
    );
}

fn scan_dir(dir: &Path, hits: &mut Vec<String>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            scan_dir(&path, hits);
            continue;
        }
        let Some(ext) = path.extension().and_then(|e| e.to_str()) else {
            continue;
        };
        if ![
            "rs", "go", "yml", "toml", "template", "sh", "env", "example",
        ]
        .contains(&ext)
        {
            continue;
        }
        let Ok(text) = std::fs::read_to_string(&path) else {
            continue;
        };
        for (n, line) in text.lines().enumerate() {
            for token in [
                "RelayMode::Default",
                "RelayMode::Staging",
                "n0.iroh.link",
                "relay.iroh.network",
                "default_relay_map",
            ] {
                if line.contains(token) {
                    hits.push(format!("{}:{}: {token}", path.display(), n + 1));
                }
            }
        }
    }
}
