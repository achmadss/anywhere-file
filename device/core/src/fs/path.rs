//! Share-relative paths, checked before anything touches the disk.
//!
//! This is the syntactic half of the path jail. It rejects names that mean different
//! things on different platforms, so that a share behaves the same whichever OS is hosting
//! it and whichever OS is asking. The load-bearing half is [`super::ShareRoot`], which
//! resolves what survives this check one component at a time against an open directory
//! handle, and that is what actually keeps a request inside the root.
//!
//! Neither half is enough alone. This one cannot see symlinks, and the other one would
//! happily open `report.txt:secret` on Windows, which is an alternate data stream and not
//! the file the grant was written about.

use std::path::PathBuf;

/// The wire separator, on every platform. A request never carries `\`.
pub const SEPARATOR: char = '/';

/// The longest a single name may be, in bytes. The common filesystem limit.
const MAX_COMPONENT: usize = 255;

/// The longest a whole share-relative path may be, in bytes.
const MAX_PATH: usize = 4096;

/// A path inside a share, known to be relative and free of surprises.
///
/// Constructed only by [`SharePath::parse`], so holding one is evidence the checks ran.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct SharePath {
    components: Vec<String>,
}

impl SharePath {
    /// The share root itself.
    pub fn root() -> Self {
        Self::default()
    }

    /// Checks a `/`-separated path from a peer.
    pub fn parse(input: &str) -> Result<Self, PathError> {
        if input.len() > MAX_PATH {
            return Err(PathError::TooLong(input.len()));
        }
        if input.starts_with(SEPARATOR) {
            return Err(PathError::Absolute);
        }

        let mut components = Vec::new();
        for part in input.split(SEPARATOR) {
            // A trailing slash, and the two spellings of the root, mean the root.
            if part.is_empty() && (components.is_empty() || input.ends_with(SEPARATOR)) {
                continue;
            }
            check_component(part)?;
            components.push(part.to_owned());
        }
        Ok(Self { components })
    }

    /// Whether this is the share root.
    pub fn is_root(&self) -> bool {
        self.components.is_empty()
    }

    /// The names, outermost first.
    pub fn components(&self) -> &[String] {
        &self.components
    }

    /// The last name, or `None` at the root.
    pub fn file_name(&self) -> Option<&str> {
        self.components.last().map(String::as_str)
    }

    /// Extends the path by one name, which is checked like any other.
    pub fn join(&self, name: &str) -> Result<Self, PathError> {
        check_component(name)?;
        let mut components = self.components.clone();
        components.push(name.to_owned());
        Ok(Self { components })
    }

    /// The path as the local OS spells it, still relative.
    ///
    /// `.` at the root, because an empty path is not a thing the OS will open.
    pub(crate) fn to_os_path(&self) -> PathBuf {
        if self.components.is_empty() {
            return PathBuf::from(".");
        }
        self.components.iter().collect()
    }
}

/// Always `/`-separated, whatever the host OS calls a separator.
impl std::fmt::Display for SharePath {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if self.components.is_empty() {
            return f.write_str(".");
        }
        for (i, part) in self.components.iter().enumerate() {
            if i > 0 {
                f.write_str("/")?;
            }
            f.write_str(part)?;
        }
        Ok(())
    }
}

/// Windows treats these as devices wherever they appear, whatever extension follows.
const RESERVED: [&str; 24] = [
    "CON", "PRN", "AUX", "NUL", "COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7",
    "COM8", "COM9", "LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
];

fn check_component(part: &str) -> Result<(), PathError> {
    if part.is_empty() {
        return Err(PathError::EmptyComponent);
    }
    if part.len() > MAX_COMPONENT {
        return Err(PathError::NameTooLong(part.to_owned()));
    }
    if part == "." || part == ".." {
        return Err(PathError::Traversal);
    }

    for c in part.chars() {
        match c {
            // `\` is a separator on Windows, so a name containing one would be one name
            // here and two there. `:` is a drive letter or an alternate data stream. `/`
            // cannot reach here from `parse`, which splits on it, but `join` takes a bare
            // name and must not accept a path.
            '\\' | ':' | '/' | '\0' => return Err(PathError::ReservedCharacter(c)),
            c if c.is_control() => return Err(PathError::ReservedCharacter(c)),
            _ => {}
        }
    }

    // Windows silently strips these, so `report.` and `report` are the same file there and
    // different files here. Allowing them would let one grant cover two names.
    if part.ends_with('.') || part.ends_with(' ') {
        return Err(PathError::TrailingDotOrSpace);
    }

    // Checked on every platform, not only Windows, so that a Linux host cannot create a
    // name that a Windows peer is then unable to open. cap-std enforces the same list, but
    // only on Windows and only at open time.
    let stem = part.split('.').next().unwrap_or(part);
    if RESERVED
        .iter()
        .any(|r| stem.trim_end().eq_ignore_ascii_case(r))
    {
        return Err(PathError::ReservedName(stem.to_owned()));
    }

    Ok(())
}

/// Why a path was refused before any syscall ran.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum PathError {
    /// A path that starts at the filesystem root, not at the share root.
    #[error("path must be relative to the share root")]
    Absolute,

    /// A `.` or `..` component.
    #[error("`.` and `..` are not accepted in a share path")]
    Traversal,

    /// Two separators in a row.
    #[error("empty path component")]
    EmptyComponent,

    /// A character that means something structural on some platform.
    #[error("{0:?} is not allowed in a share path")]
    ReservedCharacter(char),

    /// A name Windows strips characters from.
    #[error("a name may not end in a dot or a space")]
    TrailingDotOrSpace,

    /// A name Windows treats as a device.
    #[error("{0} is a reserved device name")]
    ReservedName(String),

    /// One name over the filesystem limit.
    #[error("name is longer than {MAX_COMPONENT} bytes: {0}")]
    NameTooLong(String),

    /// The whole path over the limit.
    #[error("path is {0} bytes, over the {MAX_PATH} byte limit")]
    TooLong(usize),
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(input: &str) -> SharePath {
        SharePath::parse(input).unwrap_or_else(|e| panic!("{input:?} should parse: {e}"))
    }

    fn refused(input: &str) -> PathError {
        SharePath::parse(input).unwrap_err()
    }

    #[test]
    fn ordinary_paths_parse() {
        assert_eq!(ok("a/b/c.txt").to_string(), "a/b/c.txt");
        assert_eq!(
            ok("file with spaces.txt").to_string(),
            "file with spaces.txt"
        );
        assert_eq!(ok("ünïcode/名前.txt").to_string(), "ünïcode/名前.txt");
    }

    #[test]
    fn the_empty_path_is_the_root() {
        assert!(ok("").is_root());
        assert_eq!(ok("").to_string(), ".");
        assert_eq!(
            ok("a/").components(),
            ["a"],
            "a trailing slash is not a name"
        );
    }

    #[test]
    fn traversal_is_refused_in_every_position() {
        for input in ["..", "../x", "a/../b", "a/..", "a/./b", "."] {
            assert_eq!(refused(input), PathError::Traversal, "{input:?}");
        }
    }

    #[test]
    fn absolute_paths_are_refused() {
        assert_eq!(refused("/etc/passwd"), PathError::Absolute);
        assert_eq!(refused("/"), PathError::Absolute);
    }

    #[test]
    fn windows_spellings_are_refused_on_every_platform() {
        // A backslash is a separator there and a name here.
        assert_eq!(refused(r"a\b"), PathError::ReservedCharacter('\\'));
        // A drive-relative path, and an alternate data stream.
        assert_eq!(refused("C:/x"), PathError::ReservedCharacter(':'));
        assert_eq!(
            refused("report.txt:secret"),
            PathError::ReservedCharacter(':')
        );
        // Silently stripped there, so two names would collapse into one.
        assert_eq!(refused("report."), PathError::TrailingDotOrSpace);
        assert_eq!(refused("report "), PathError::TrailingDotOrSpace);
        // Device names, with and without an extension.
        assert!(matches!(refused("NUL"), PathError::ReservedName(_)));
        assert!(matches!(refused("a/con.txt"), PathError::ReservedName(_)));
        assert!(matches!(refused("Aux"), PathError::ReservedName(_)));
    }

    #[test]
    fn a_short_name_is_just_a_name_here() {
        // `PROGRA~1` is Windows' 8.3 alias for a long name. It cannot leave the share
        // root, because resolution is against a directory handle rather than a string, so
        // it is allowed through as an ordinary name and resolves to whatever it aliases.
        assert_eq!(ok("PROGRA~1/x").to_string(), "PROGRA~1/x");
    }

    #[test]
    fn control_characters_and_nul_are_refused() {
        assert_eq!(refused("a\0b"), PathError::ReservedCharacter('\0'));
        assert_eq!(refused("a\nb"), PathError::ReservedCharacter('\n'));
    }

    #[test]
    fn oversized_names_are_refused() {
        let long = "x".repeat(256);
        assert!(matches!(refused(&long), PathError::NameTooLong(_)));

        let deep = (0..2000).map(|_| "dir").collect::<Vec<_>>().join("/");
        assert!(matches!(refused(&deep), PathError::TooLong(_)));
    }

    #[test]
    fn double_separators_are_refused() {
        assert_eq!(refused("a//b"), PathError::EmptyComponent);
    }

    #[test]
    fn join_checks_the_name_it_is_given() {
        let base = ok("a");
        assert_eq!(base.join("b").unwrap().to_string(), "a/b");
        assert_eq!(base.join("..").unwrap_err(), PathError::Traversal);
        assert_eq!(
            base.join("b/c").unwrap_err(),
            PathError::ReservedCharacter('/')
        );
    }
}
