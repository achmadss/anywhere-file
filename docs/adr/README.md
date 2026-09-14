# Architecture decision records

One file per decision that would otherwise be re-argued. Numbered, append-only: a decision
that turns out wrong gets a new record that supersedes the old one, and the old one stays
where it is with a note at the top. The record of a reversal is worth more than a tidy
directory. 0001 is itself a reversal, and the reasoning that produced the first answer is
the reason the second one is trustworthy.

A record is worth writing when the answer was not obvious, an alternative was seriously
considered, or someone will ask "why not X" later. Not for every choice.

| | |
|---|---|
| [0001](0001-rust-iroh-go-control-plane.md) | Rust core on iroh, Go control plane, plus the Go + libp2p alternative |
| [0002](0002-transport-seam.md) | The transport seam: what `rfm-core` may use from iroh |
| [0003](0003-own-devices-pair-as-admin.md) | Your own devices pair as admins, and what that widens |
| [0004](0004-the-upgrade-moment.md) | The 14-day trial, offered where remote access is missed |
