# The path jail

r3 §9 step 5, issue [#13](https://github.com/achmadss/anywhere-file/issues/13). Code in
`device/core/src/fs/`, attacked in `device/core/tests/containment.rs`.

Every filesystem operation is against one share root and takes a share-relative path. What
the operation touches has to be inside that root once symlinks are resolved, and still
inside it when the syscall runs.

## Two halves

`SharePath::parse` runs first. It rejects `.` and `..`, absolute paths, `\` and `:`, control
characters, names ending in a dot or a space, the Windows device names, and anything over
255 bytes per name or 4096 bytes in total. These are rejected on every platform, not only
on Windows, so a share holds the same set of reachable names wherever it is hosted. A Linux
host cannot create `report.` and `report` as two files that a Windows peer then sees as one.

`ShareRoot` does the rest, through `cap-std`. It holds an open handle to the root and walks
each path one component at a time against that handle, reading and re-resolving symlinks
itself. On Linux that is `openat` per component. There is no window between deciding a path
is acceptable and opening it, because the deciding is the opening, so the classic swap of a
directory for a symlink between the check and the open has nowhere to land.

Ambient authority is used exactly once, in `ShareRoot::open`. After that a caller holding a
`ShareRoot` cannot reach outside it whatever path it passes.

## Decisions

A rename resolves both ends against the same handle, so moving a file out of its share is
not expressible. Moving between two shares is two grants, so it is two operations the
caller composes out of read, write and delete, and each one succeeds or fails visibly.

A rename that crosses a filesystem fails with `CrossDevice` and does not become a copy. A
share root can contain a mountpoint, so this is reachable. Turning an operation that is
normally instant into a multi-gigabyte copy is a surprise, it cannot be made atomic, and a
failure partway leaves both names on disk.

`delete` removes one file, one symlink, or one empty directory. Recursive delete is the
caller's to build from a listing, so a mistake costs one name and the UI can show progress.
Deleting a symlink removes the link and not what it points at.

`read` returns a reader and `write` takes one, so a 10 GB file costs a file handle and the
caller's copy buffer. `list` returns at most 65536 entries and says when it truncated.

## What this does not cover

A hardlink inside the share to a file outside it is readable. Every name for an inode is
equal and the filesystem offers nothing to tell them apart. Making one needs write access
to the share root and read access to the target, which is a person at the keyboard rather
than a peer. `containment.rs` asserts this behaviour so a change to it is visible.

Case folding is the host filesystem's business. It changes which file a name finds and
never whether the answer is inside the root. Grants (#12) are matched against a path string
and will need their own answer.

Cancelling an operation stops the next syscall and not the one in flight. No portable
syscall here is interruptible, and a half-applied rename is not something the OS offers to
undo. Transfers are chunked (#16), so a cancel costs one chunk.

Symlink attacks are tested on Unix only. Creating a symlink on Windows needs a privilege an
ordinary account does not have, so the attack is not available to an unprivileged process
there either.
