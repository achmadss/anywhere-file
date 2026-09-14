//! The path jail, attacked. r3 §9 step 5.
//!
//! Every test here asserts a denial, and each one names an attack from #13. This file is
//! the artifact that says the boundary holds, so a change that widens it should fail here
//! before it reaches review.
//!
//! Symlink attacks are Unix-only, because creating a symlink on Windows needs a privilege
//! an ordinary user does not have, so the attack is not available to an unprivileged peer
//! there either. Everything else runs on all three.

use std::{fs, io::Write, path::PathBuf};

use rfm_core::fs::{FsError, PathError, SharePath, ShareRoot};
use tokio::io::AsyncReadExt;

/// A share root with a file in it, and a sibling directory outside it holding a secret.
struct Fixture {
    _tmp: tempfile::TempDir,
    share: ShareRoot,
    root: PathBuf,
    outside: PathBuf,
}

const SECRET: &str = "outside the share";
const INSIDE: &str = "inside the share";

fn fixture() -> Fixture {
    let tmp = tempfile::tempdir().unwrap();
    let root = tmp.path().join("share");
    let outside = tmp.path().join("private");
    fs::create_dir(&root).unwrap();
    fs::create_dir(&outside).unwrap();
    fs::write(outside.join("secret.txt"), SECRET).unwrap();
    fs::write(root.join("public.txt"), INSIDE).unwrap();

    let share = ShareRoot::open(&root).unwrap();
    Fixture {
        _tmp: tmp,
        share,
        root,
        outside,
    }
}

async fn read_all(share: &ShareRoot, path: &SharePath) -> Result<String, FsError> {
    let mut reader = share.read(path, 0, None).await?;
    let mut out = String::new();
    reader.read_to_string(&mut out).await.unwrap();
    Ok(out)
}

fn path(s: &str) -> SharePath {
    SharePath::parse(s).unwrap()
}

#[tokio::test]
async fn the_baseline_read_works() {
    // Without this the rest of the file could pass by refusing everything.
    let f = fixture();
    assert_eq!(
        read_all(&f.share, &path("public.txt")).await.unwrap(),
        INSIDE
    );
}

#[tokio::test]
async fn dotdot_is_refused_before_a_syscall() {
    for attempt in ["../private/secret.txt", "..", "a/../../private/secret.txt"] {
        assert_eq!(
            SharePath::parse(attempt).unwrap_err(),
            PathError::Traversal,
            "{attempt}"
        );
    }
}

#[tokio::test]
async fn dotdot_is_refused_again_by_the_resolver() {
    // The second half of the jail, tested without the first. `SharePath` cannot express
    // this, so the raw OS path goes straight to the resolver. If the syntactic check were
    // ever loosened, this is what would still hold.
    let f = fixture();
    let dir = cap_std::fs::Dir::open_ambient_dir(&f.root, cap_std::ambient_authority()).unwrap();
    let escaped = dir.open("../private/secret.txt");
    assert!(escaped.is_err(), "cap-std followed `..` out of the root");
}

#[cfg(unix)]
#[tokio::test]
async fn a_symlink_pointing_outside_is_refused() {
    let f = fixture();
    std::os::unix::fs::symlink(f.outside.join("secret.txt"), f.root.join("escape.txt")).unwrap();

    let err = read_all(&f.share, &path("escape.txt")).await.unwrap_err();
    assert!(matches!(err, FsError::Escaped), "{err:?}");
}

#[cfg(unix)]
#[tokio::test]
async fn a_symlinked_directory_outside_is_refused() {
    let f = fixture();
    std::os::unix::fs::symlink(&f.outside, f.root.join("out")).unwrap();

    let err = read_all(&f.share, &path("out/secret.txt"))
        .await
        .unwrap_err();
    assert!(matches!(err, FsError::Escaped), "{err:?}");

    let err = f.share.list(&path("out")).await.unwrap_err();
    assert!(matches!(err, FsError::Escaped), "{err:?}");
}

#[cfg(unix)]
#[tokio::test]
async fn an_absolute_symlink_is_refused() {
    let f = fixture();
    std::os::unix::fs::symlink("/etc/passwd", f.root.join("passwd")).unwrap();

    let err = read_all(&f.share, &path("passwd")).await.unwrap_err();
    assert!(matches!(err, FsError::Escaped), "{err:?}");
}

#[cfg(unix)]
#[tokio::test]
async fn a_chain_of_symlinks_out_is_refused() {
    let f = fixture();
    // Each hop is inside the root; only the last one leaves.
    std::os::unix::fs::symlink(f.outside.join("secret.txt"), f.root.join("hop2")).unwrap();
    std::os::unix::fs::symlink("hop2", f.root.join("hop1")).unwrap();

    let err = read_all(&f.share, &path("hop1")).await.unwrap_err();
    assert!(matches!(err, FsError::Escaped), "{err:?}");
}

#[cfg(unix)]
#[tokio::test]
async fn a_symlink_that_stays_inside_still_works() {
    // The jail has to be a boundary and not a ban: a share full of symlinks between its
    // own folders is ordinary, and refusing those would be a different bug.
    let f = fixture();
    std::os::unix::fs::symlink("public.txt", f.root.join("alias.txt")).unwrap();

    assert_eq!(
        read_all(&f.share, &path("alias.txt")).await.unwrap(),
        INSIDE
    );
}

#[cfg(unix)]
#[tokio::test]
async fn a_symlink_is_listed_as_a_symlink_and_not_followed() {
    let f = fixture();
    std::os::unix::fs::symlink(&f.outside, f.root.join("out")).unwrap();

    let listing = f.share.list(&SharePath::root()).await.unwrap();
    let entry = listing.entries.iter().find(|e| e.name == "out").unwrap();
    assert_eq!(entry.kind, rfm_core::fs::EntryKind::Symlink);
}

#[cfg(unix)]
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn swapping_a_component_for_a_symlink_under_load_never_leaks() {
    // TOCTOU. One task swaps `swap` between a real directory holding an innocent file and
    // a symlink to the private directory holding a file of the same name. Another reads
    // `swap/secret.txt` as fast as it can. A check-then-open resolver loses this race; a
    // resolver that walks handles cannot, because there is no gap to land in.
    let f = fixture();
    let root = f.root.clone();
    let outside = f.outside.clone();

    let swapper = std::thread::spawn(move || {
        let swap = root.join("swap");
        for _ in 0..3_000 {
            let _ = fs::remove_file(&swap);
            let _ = fs::remove_dir_all(&swap);
            if fs::create_dir(&swap).is_ok() {
                let _ = fs::write(swap.join("secret.txt"), INSIDE);
            }
            let _ = fs::remove_dir_all(&swap);
            let _ = std::os::unix::fs::symlink(&outside, &swap);
        }
    });

    let target = path("swap/secret.txt");
    let mut reads = 0u32;
    while !swapper.is_finished() {
        match read_all(&f.share, &target).await {
            Ok(content) => {
                // An empty read is the swapper caught between creating the file and
                // writing it. The property under test is that the bytes are never the
                // ones from outside the share.
                assert_ne!(content, SECRET, "the private file was served");
                if content == INSIDE {
                    reads += 1;
                }
            }
            // Every way the walk can notice the ground moved under it. `InvalidInput`
            // is the interesting one: the resolver saw a symlink, went to read it, and
            // by then it was a directory again. That is the race being lost safely.
            Err(FsError::NotFound | FsError::Escaped | FsError::NotADirectory) => {}
            Err(FsError::IsADirectory | FsError::AlreadyExists) => {}
            Err(FsError::Io(ref e)) if e.kind() == std::io::ErrorKind::InvalidInput => {}
            Err(other) => panic!("unexpected error during the race: {other:?}"),
        }
    }
    swapper.join().unwrap();
    assert!(reads > 0, "the race never once resolved to the real file");
}

#[cfg(unix)]
#[tokio::test]
async fn a_hardlink_to_a_file_outside_is_readable_and_that_is_the_documented_limit() {
    // A hardlink is a name, and every name for an inode is equal. Nothing in the
    // filesystem tells the difference between the share's copy and the original, so this
    // jail cannot refuse it. Making one needs write access to the share root and read
    // access to the target, which is a person at the keyboard rather than a peer.
    // Recorded here rather than only in prose, so it fails loudly if it ever changes.
    let f = fixture();
    if fs::hard_link(f.outside.join("secret.txt"), f.root.join("linked.txt")).is_err() {
        return; // The filesystem refused the link; nothing to assert.
    }
    assert_eq!(
        read_all(&f.share, &path("linked.txt")).await.unwrap(),
        SECRET
    );
}

#[tokio::test]
async fn windows_spellings_are_refused_on_every_platform() {
    // Alternate data stream, UNC prefix, drive-relative path, and a trailing dot that
    // Windows strips. All refused here whatever the host OS, so a share holds the same
    // set of reachable names everywhere.
    assert!(SharePath::parse("public.txt:secret").is_err());
    assert!(SharePath::parse(r"\\server\share\x").is_err());
    assert!(SharePath::parse("C:/Windows/win.ini").is_err());
    assert!(SharePath::parse("public.txt.").is_err());
    assert!(SharePath::parse("NUL").is_err());
}

#[tokio::test]
async fn a_short_name_cannot_reach_outside() {
    // `PROGRA~1` is a name like any other to a handle-based resolver: it is looked up in
    // the directory it appears in, so the worst it can do is alias something already
    // inside the share.
    let f = fixture();
    let err = read_all(&f.share, &path("PROGRA~1/secret.txt"))
        .await
        .unwrap_err();
    assert!(matches!(err, FsError::NotFound), "{err:?}");
}

#[tokio::test]
async fn case_does_not_open_a_way_out() {
    // On a case-insensitive filesystem `PUBLIC.TXT` and `public.txt` are one file. Either
    // way the lookup happens inside the root handle, so case changes which file is found
    // and never whether the answer is inside.
    let f = fixture();
    match read_all(&f.share, &path("PUBLIC.TXT")).await {
        Ok(content) => assert_eq!(content, INSIDE, "case-insensitive host"),
        Err(FsError::NotFound) => {}
        Err(other) => panic!("{other:?}"),
    }
    // And no spelling of the escape survives the syntactic check, upper case included.
    assert_eq!(
        SharePath::parse("../PRIVATE/secret.txt").unwrap_err(),
        PathError::Traversal
    );
}

#[tokio::test]
async fn a_rename_cannot_move_a_file_out() {
    let f = fixture();
    // Both ends resolve against the share handle, so the only expressible move is inside.
    assert!(SharePath::parse("../private/stolen.txt").is_err());

    f.share
        .rename(&path("public.txt"), &path("moved.txt"))
        .await
        .unwrap();
    assert_eq!(
        read_all(&f.share, &path("moved.txt")).await.unwrap(),
        INSIDE
    );
    assert!(!f.outside.join("stolen.txt").exists());
}

#[tokio::test]
async fn deleting_the_share_root_is_refused() {
    let f = fixture();
    let err = f.share.delete(&SharePath::root()).await.unwrap_err();
    assert!(matches!(err, FsError::Denied), "{err:?}");
    assert!(f.root.exists());
}

#[cfg(unix)]
#[tokio::test]
async fn deleting_a_symlink_removes_the_link_and_not_the_target() {
    let f = fixture();
    std::os::unix::fs::symlink(f.outside.join("secret.txt"), f.root.join("escape.txt")).unwrap();

    f.share.delete(&path("escape.txt")).await.unwrap();
    assert!(!f.root.join("escape.txt").exists());
    assert!(
        f.outside.join("secret.txt").exists(),
        "the delete followed the link out of the share"
    );
}

#[tokio::test]
async fn a_ten_gigabyte_file_streams_in_bounded_memory() {
    // The acceptance criterion in #13. The file is sparse, so this costs no disk; the
    // point is that `read` hands back a reader and never a `Vec`, so the whole file is
    // never anywhere at once.
    const TEN_GIB: u64 = 10 * 1024 * 1024 * 1024;

    let f = fixture();
    let mut big = fs::File::create(f.root.join("big.bin")).unwrap();
    big.write_all(b"head").unwrap();
    big.set_len(TEN_GIB).unwrap();
    drop(big);

    let before = peak_rss();
    let reader = f.share.read(&path("big.bin"), 0, None).await.unwrap();
    // A megabyte at a time, because `tokio::fs` costs a thread hop per read and eight
    // kilobytes at a time would be a million of them.
    let mut reader = tokio::io::BufReader::with_capacity(1024 * 1024, reader);
    let copied = tokio::io::copy_buf(&mut reader, &mut tokio::io::sink())
        .await
        .unwrap();
    assert_eq!(copied, TEN_GIB);

    if let (Some(before), Some(after)) = (before, peak_rss()) {
        let grew = after.saturating_sub(before);
        assert!(
            grew < 256 * 1024 * 1024,
            "peak memory grew by {grew} bytes streaming a 10 GiB file"
        );
    }
}

/// Peak resident set size in bytes, where the OS makes it cheap to ask.
fn peak_rss() -> Option<u64> {
    #[cfg(target_os = "linux")]
    {
        let status = fs::read_to_string("/proc/self/status").ok()?;
        let line = status.lines().find(|l| l.starts_with("VmHWM:"))?;
        let kb: u64 = line.split_whitespace().nth(1)?.parse().ok()?;
        Some(kb * 1024)
    }
    #[cfg(not(target_os = "linux"))]
    {
        None
    }
}
