//! Where an agent keeps its state on disk, and how it gets there without tearing.

use std::{
    fs, io,
    path::{Path, PathBuf},
};

use tempfile::NamedTempFile;

/// One agent's state directory.
///
/// ```text
/// <root>/
///   identity/          device key material (#6)
///   trust/             one signed trust list per workspace, `<workspace-id>.tl` (#7)
///   transfers/         resumable transfer state (#16)
///   shares.json        share roots and grants (#12)
/// ```
///
/// The subdirectories exist from the moment the directory is opened, so nothing below has
/// to create them on the way to a write. The agent chooses the root (#33); the platform
/// data directory is its business, not this crate's.
#[derive(Debug, Clone)]
pub struct ConfigDir {
    root: PathBuf,
}

impl ConfigDir {
    /// Creates the layout if it is not there, then returns a handle to it.
    pub fn open(root: impl Into<PathBuf>) -> io::Result<Self> {
        let dir = Self { root: root.into() };
        for path in [dir.identity_dir(), dir.trust_dir(), dir.transfers_dir()] {
            fs::create_dir_all(path)?;
        }
        Ok(dir)
    }

    /// The directory the layout is rooted at.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Device key material (#6).
    pub fn identity_dir(&self) -> PathBuf {
        self.root.join("identity")
    }

    /// Signed trust lists, one file per workspace (#7). A device is in several workspaces
    /// at once (r3 §11.1), so this is a directory rather than a file.
    pub fn trust_dir(&self) -> PathBuf {
        self.root.join("trust")
    }

    /// Resumable transfer state (#16).
    pub fn transfers_dir(&self) -> PathBuf {
        self.root.join("transfers")
    }

    /// Share roots and grants (#12).
    pub fn shares_file(&self) -> PathBuf {
        self.root.join("shares.json")
    }
}

/// Replaces `path` with `bytes`, or leaves it exactly as it was.
///
/// r3 §8.1 requires atomic writes for the trust list, and everything else in the layout
/// wants the same guarantee for the same reason: a torn file is indistinguishable from a
/// forged one until you have parsed it, and parsing it is the thing you were trying to do.
///
/// The bytes go to a uniquely named temporary file in the destination's own directory, are
/// fsynced, and are then renamed over the destination. On Unix the directory is fsynced
/// after the rename, so the rename itself survives a power cut; Windows has no directory
/// handle to sync and `MoveFileEx` is atomic on its own.
///
/// **What a leftover `.tmp` file means on restart:** a crash between create and rename. It
/// is never the current state of anything, no reader ever opens it (readers only open the
/// destination name), and deleting it is always safe. The previous contents of the
/// destination are intact.
pub fn write_atomic(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let dir = path.parent().ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            "atomic write needs a path with a parent directory",
        )
    })?;

    let mut tmp = NamedTempFile::with_suffix_in(".tmp", dir)?;
    io::Write::write_all(&mut tmp, bytes)?;
    tmp.as_file().sync_all()?;
    tmp.persist(path)?;

    #[cfg(unix)]
    fs::File::open(dir)?.sync_all()?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn open_creates_the_layout_and_is_idempotent() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path().join("state")).unwrap();
        ConfigDir::open(tmp.path().join("state")).unwrap();

        assert!(dir.identity_dir().is_dir());
        assert!(dir.trust_dir().is_dir());
        assert!(dir.transfers_dir().is_dir());
    }

    #[test]
    fn write_atomic_replaces_and_leaves_no_temporary_behind() {
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let path = dir.trust_dir().join("ws.tl");

        write_atomic(&path, b"version 1").unwrap();
        write_atomic(&path, b"version 2").unwrap();

        assert_eq!(fs::read(&path).unwrap(), b"version 2");
        let leftovers: Vec<_> = fs::read_dir(dir.trust_dir())
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .filter(|name| name != "ws.tl")
            .collect();
        assert!(
            leftovers.is_empty(),
            "temporaries left behind: {leftovers:?}"
        );
    }

    #[test]
    fn a_failed_write_leaves_the_previous_contents() {
        // The destination is a directory, so the rename cannot succeed. The point is what
        // the reader sees afterwards, which is the old file and not a truncated one.
        let tmp = tempfile::tempdir().unwrap();
        let dir = ConfigDir::open(tmp.path()).unwrap();
        let path = dir.trust_dir().join("ws.tl");
        write_atomic(&path, b"version 1").unwrap();

        let blocked = dir.trust_dir().join("blocked");
        fs::create_dir(&blocked).unwrap();
        assert!(write_atomic(&blocked, b"version 2").is_err());
        assert_eq!(fs::read(&path).unwrap(), b"version 1");
    }
}
