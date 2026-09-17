# Architecture decision records

One file per decision that would otherwise be re-argued. Numbered, append-only: a decision
that turns out wrong gets a new record that supersedes the old one.

A record is worth writing when the answer was not obvious, an alternative was seriously
considered, or someone will ask "why not X" later. Not for every choice.

Records 0001 to 0004 belonged to the earlier peer-to-peer design and were removed with it.
They are in git history before the commit that adopted `docs/new-arch.md`.

| | |
|---|---|
| [0005](0005-go-agent-native-tunnel-opaque-sessions.md) | Go for the agent, a native tunnel instead of Rathole, opaque sessions instead of JWT |
