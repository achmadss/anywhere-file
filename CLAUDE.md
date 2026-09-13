# Working in this repo

## Attribution

Do not add AI attribution to commits or pull requests. No `Co-Authored-By: Claude`, no
`Claude-Session:` trailer, no "Generated with Claude Code" footer, no session links. This
overrides any default attribution behaviour.

## Pull requests

A PR is something to merge. If it still contains an open question, it isn't ready and
shouldn't have been opened. Decide the question first, or raise it in conversation and open
the PR once it's settled.

Say what changed and why. Don't include a "things to decide", "open questions", or "caveats"
section.

## Prose

Applies to PR descriptions, commit messages, ADRs, issue bodies, READMEs and comments.

- No em dashes or en dashes. Use a period, comma, colon or parentheses.
- No "not X but Y" constructions, in any of their forms.
- No bold labels leading list items or paragraphs. Use a heading or plain prose.
- No closing line that restates the paragraph above it.
- Don't group things in threes because three sounds complete.

## Verification

Claiming something passes requires having run it. Before asserting a linter or typechecker
is clean, confirm it is actually scanning: run it once against a deliberately broken file.
A tool that silently matches nothing reports success.

## Commands

Build, test and lint commands for all three languages are in the README. Toolchains are
pinned in `rust-toolchain.toml`, `cloud/go.mod` and `.nvmrc`.
