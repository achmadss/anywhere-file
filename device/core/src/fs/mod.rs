//! The only part of the system that touches the disk (r3 §12).
//!
//! Every operation is against one share root and takes a share-relative path. The
//! containment rule is §9 step 5: what the operation ends up touching must be inside the
//! root after symlinks are resolved, and it must still be inside the root at the moment
//! the syscall runs, not merely at the moment it was checked.
//!
//! Resolution is `cap-std`'s, which walks the path one component at a time against an open
//! directory handle, reads and re-resolves symlinks itself, and never hands a whole string
//! to the OS. On Linux that is `openat` per component. This is what makes the check
//! TOCTOU-free: there is no window between deciding a path is fine and opening it, because
//! the deciding *is* the opening. A `..` that would leave the handle, and a symlink that
//! points outside it, both fail during the walk.
//!
//! [`SharePath`] runs first and rejects names that mean different things on different
//! platforms. `docs/path-jail.md` records what this jail does not cover.

mod path;

use std::{
    io::{self, Seek, SeekFrom},
    path::Path,
    sync::Arc,
    time::SystemTime,
};

use cap_std::{ambient_authority, fs::Dir};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWriteExt};

pub use self::path::{PathError, SEPARATOR, SharePath};

/// The most entries one `list` returns. Past this the listing is marked truncated rather
/// than growing without bound, because a share root is someone else's directory and can
/// hold as many names as their filesystem allows.
pub const LIST_LIMIT: usize = 65_536;

/// One folder exposed to the workspace, and the boundary requests are held inside.
///
/// Cloning is cheap and shares the same directory handle.
#[derive(Debug, Clone)]
pub struct ShareRoot {
    dir: Arc<Dir>,
}

impl ShareRoot {
    /// Opens a directory as a share root.
    ///
    /// The one call in this module that names an absolute path, and the only place
    /// ambient authority is used. Everything afterwards goes through the handle this
    /// returns, so a caller holding a [`ShareRoot`] cannot reach outside it whatever path
    /// it passes.
    pub fn open(root: impl AsRef<Path>) -> Result<Self, FsError> {
        let dir = Dir::open_ambient_dir(root, ambient_authority()).map_err(map_io)?;
        Ok(Self { dir: Arc::new(dir) })
    }

    /// What one name is, without following it if it is a symlink.
    pub async fn stat(&self, path: &SharePath) -> Result<Entry, FsError> {
        let (dir, rel) = self.parts(path);
        let name = path.file_name().unwrap_or(".").to_owned();
        blocking(move || {
            let meta = dir.symlink_metadata(&rel)?;
            Ok(Entry::from_metadata(name, &meta))
        })
        .await
    }

    /// The entries directly inside a directory.
    ///
    /// Symlinks are reported as symlinks and are not followed, so a listing cannot be made
    /// slow, or made to loop, by a directory full of links.
    pub async fn list(&self, path: &SharePath) -> Result<Listing, FsError> {
        let (dir, rel) = self.parts(path);
        blocking(move || {
            let mut entries = Vec::new();
            let mut truncated = false;
            for entry in dir.read_dir(&rel)? {
                let entry = entry?;
                if entries.len() == LIST_LIMIT {
                    truncated = true;
                    break;
                }
                let name = entry.file_name().to_string_lossy().into_owned();
                let meta = entry.metadata()?;
                entries.push(Entry::from_metadata(name, &meta));
            }
            Ok(Listing { entries, truncated })
        })
        .await
    }

    /// Opens a byte range of a file for reading.
    ///
    /// Returns a reader rather than the bytes, so a 10 GB file costs one file handle and
    /// whatever buffer the caller copies through. `len` of `None` reads to the end.
    pub async fn read(
        &self,
        path: &SharePath,
        offset: u64,
        len: Option<u64>,
    ) -> Result<impl AsyncRead + Unpin + use<>, FsError> {
        let (dir, rel) = self.parts(path);
        let file = blocking(move || {
            let mut file = dir.open(&rel)?.into_std();
            if offset != 0 {
                file.seek(SeekFrom::Start(offset))?;
            }
            Ok(file)
        })
        .await?;
        Ok(tokio::fs::File::from_std(file).take(len.unwrap_or(u64::MAX)))
    }

    /// Writes a byte range of a file, creating it if it is not there.
    ///
    /// Returns the number of bytes written. The file is on disk when this returns; #16
    /// calls it once per resumable span rather than once per chunk, so that is one `fsync`
    /// per span and not one per network packet.
    pub async fn write<R>(&self, path: &SharePath, offset: u64, src: &mut R) -> Result<u64, FsError>
    where
        R: AsyncRead + Unpin + ?Sized,
    {
        let (dir, rel) = self.parts(path);
        let file = blocking(move || {
            let mut file = dir
                .open_with(
                    &rel,
                    cap_std::fs::OpenOptions::new().write(true).create(true),
                )?
                .into_std();
            if offset != 0 {
                file.seek(SeekFrom::Start(offset))?;
            }
            Ok(file)
        })
        .await?;

        let mut file = tokio::fs::File::from_std(file);
        let written = tokio::io::copy(src, &mut file).await.map_err(map_io)?;
        file.flush().await.map_err(map_io)?;
        file.sync_all().await.map_err(map_io)?;
        Ok(written)
    }

    /// Removes a file, a symlink, or an empty directory.
    ///
    /// A directory with anything in it is refused. Recursive delete is the caller's to
    /// compose out of a listing and one call per entry, so a mistake costs one name rather
    /// than a subtree, and so progress and cancellation are visible.
    pub async fn delete(&self, path: &SharePath) -> Result<(), FsError> {
        if path.is_root() {
            return Err(FsError::Denied);
        }
        let (dir, rel) = self.parts(path);
        blocking(move || {
            // `symlink_metadata` so that deleting a symlink removes the link and not
            // whatever it points at.
            if dir.symlink_metadata(&rel)?.is_dir() {
                dir.remove_dir(&rel)
            } else {
                dir.remove_file(&rel)
            }
        })
        .await
    }

    /// Creates one directory. The parent must already exist.
    pub async fn mkdir(&self, path: &SharePath) -> Result<(), FsError> {
        let (dir, rel) = self.parts(path);
        blocking(move || dir.create_dir(&rel)).await
    }

    /// Renames within this share root.
    ///
    /// Both ends resolve against the same handle, so a move out of the share is not
    /// expressible. A move to another share is two grants and therefore two operations:
    /// the caller reads, writes, and deletes, and sees each one succeed or fail.
    ///
    /// A rename that crosses a filesystem, which a share root containing a mountpoint can
    /// produce, fails with [`FsError::CrossDevice`] rather than turning into a copy. A
    /// copy of a large file behind an operation that is normally instant is a surprise,
    /// it is not atomic, and a failure halfway leaves both names present.
    pub async fn rename(&self, from: &SharePath, to: &SharePath) -> Result<(), FsError> {
        if from.is_root() || to.is_root() {
            return Err(FsError::Denied);
        }
        let dir = self.dir.clone();
        let (from, to) = (from.to_os_path(), to.to_os_path());
        blocking(move || {
            let target = dir.clone();
            dir.rename(&from, &target, &to)
        })
        .await
    }

    fn parts(&self, path: &SharePath) -> (Arc<Dir>, std::path::PathBuf) {
        (self.dir.clone(), path.to_os_path())
    }
}

/// What one name in a share is.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    /// The name on its own, with no directory part.
    pub name: String,
    /// File, directory, symlink, or something the OS offers that we do not serve.
    pub kind: EntryKind,
    /// Size in bytes. Zero for anything that is not a file.
    pub len: u64,
    /// Last modification, where the filesystem records one.
    pub modified: Option<SystemTime>,
    /// Whether the OS says this device cannot write it.
    pub readonly: bool,
}

impl Entry {
    fn from_metadata(name: String, meta: &cap_std::fs::Metadata) -> Self {
        let kind = if meta.is_symlink() {
            EntryKind::Symlink
        } else if meta.is_dir() {
            EntryKind::Dir
        } else if meta.is_file() {
            EntryKind::File
        } else {
            EntryKind::Other
        };
        Self {
            name,
            kind,
            len: if kind == EntryKind::File {
                meta.len()
            } else {
                0
            },
            modified: meta.modified().ok().map(|t| t.into_std()),
            readonly: meta.permissions().readonly(),
        }
    }
}

/// The kinds of thing a share can hold.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EntryKind {
    /// A regular file.
    File,
    /// A directory.
    Dir,
    /// A symlink, reported without being followed.
    Symlink,
    /// A socket, a device node, a FIFO. Listed so it is visible, never opened.
    Other,
}

/// One directory's contents.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Listing {
    /// The entries, in whatever order the filesystem gave them.
    pub entries: Vec<Entry>,
    /// Whether [`LIST_LIMIT`] cut the listing short.
    pub truncated: bool,
}

/// What the UI shows when an operation fails.
///
/// Named cases rather than an errno, because the UI has to say something a person can act
/// on and because a peer must not learn the host's directory layout from an error string.
#[derive(Debug, thiserror::Error)]
pub enum FsError {
    /// The path was refused before any syscall ran.
    #[error(transparent)]
    Path(#[from] PathError),

    /// Resolution left the share root. A `..` past the top, or a symlink pointing out.
    #[error("path leaves the share")]
    Escaped,

    /// No such file or directory.
    #[error("not found")]
    NotFound,

    /// The operating system refused.
    #[error("denied by the operating system")]
    Denied,

    /// A file where a directory was needed.
    #[error("not a directory")]
    NotADirectory,

    /// A directory where a file was needed.
    #[error("is a directory")]
    IsADirectory,

    /// Something is already there.
    #[error("already exists")]
    AlreadyExists,

    /// A directory with entries in it.
    #[error("directory is not empty")]
    NotEmpty,

    /// The filesystem is full.
    #[error("no space left")]
    NoSpace,

    /// The filesystem refused the name.
    #[error("name too long")]
    NameTooLong,

    /// Another process holds it.
    #[error("in use")]
    InUse,

    /// A rename between two filesystems.
    #[error("cannot rename across filesystems")]
    CrossDevice,

    /// Anything else the OS reported.
    #[error("filesystem error: {0}")]
    Io(io::Error),
}

/// Runs a blocking filesystem call off the async runtime.
///
/// Cancelling the returned future stops the next call, and not the one already in flight:
/// no portable syscall here is interruptible, and a partially applied `rename` is not a
/// thing the OS offers to take back. Transfers are chunked (#16), so the delay a caller
/// sees on cancel is one chunk, and the operations that are not chunked are single
/// syscalls that either happened or did not.
async fn blocking<T, F>(f: F) -> Result<T, FsError>
where
    F: FnOnce() -> io::Result<T> + Send + 'static,
    T: Send + 'static,
{
    match tokio::task::spawn_blocking(f).await {
        Ok(result) => result.map_err(map_io),
        Err(joined) => Err(FsError::Io(io::Error::other(joined))),
    }
}

fn map_io(e: io::Error) -> FsError {
    use io::ErrorKind as K;

    match e.kind() {
        // cap-std builds its escape error by hand, with no errno behind it, so a
        // `PermissionDenied` carrying a raw code came from the OS and one without came
        // from the sandbox. The distinction matters: one is a wrong answer to give a
        // peer, the other is an attack worth logging.
        K::PermissionDenied if e.raw_os_error().is_none() => FsError::Escaped,
        K::PermissionDenied => FsError::Denied,
        K::NotFound => FsError::NotFound,
        K::AlreadyExists => FsError::AlreadyExists,
        K::NotADirectory => FsError::NotADirectory,
        K::IsADirectory => FsError::IsADirectory,
        K::DirectoryNotEmpty => FsError::NotEmpty,
        K::StorageFull => FsError::NoSpace,
        K::InvalidFilename => FsError::NameTooLong,
        K::ResourceBusy => FsError::InUse,
        K::CrossesDevices => FsError::CrossDevice,
        _ => FsError::Io(e),
    }
}
