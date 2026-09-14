# Delegating issues

How work gets handed to an implementer and comes back as a pull request. The implementer
is the OpenCode CLI, driven by the `opencode-delegate` skill. Whoever delegated reviews the
result and makes the commit. OpenCode never commits.

## Pick what is ready

Blockers live on the issues themselves as GitHub issue dependencies, set from the table in
#41. `.github/scripts/check-blockers.sh` reads that same graph in CI, so there is no second
copy to drift.

```sh
repo=$(gh repo view --json nameWithOwner -q .nameWithOwner)
for i in $(gh issue list --limit 100 --json number --jq '.[].number'); do
  open=$(gh api "repos/$repo/issues/$i/dependencies/blocked_by" \
           --jq '[.[] | select(.state == "open") | .number] | join(",")')
  [ -z "$open" ] && echo "#$i ready"
done
```

Five at a time is a comfortable batch. Past that, review becomes the bottleneck and the
branches start colliding faster than they land.

## Give each run its own worktree

Two runs editing one checkout will overwrite each other. One worktree per issue:

```sh
git worktree add -b issue/20-cloud-accounts /path/to/wt/20 main
```

Runs that share a resource need it split too. Two `cloud/` issues both drop and recreate the
`public` schema, so each gets its own database on the one running PostgreSQL, and both briefs
say not to touch `docker compose`:

```sh
docker compose -f cloud/docker-compose.yml up -d
docker compose -f cloud/docker-compose.yml exec -T postgres \
  psql -U rfm -d rfm -c 'CREATE DATABASE rfm_t20 OWNER rfm;'
```

Migration numbers collide the same way. Two branches off main will both reach for `0002`.
`loadMigrations` sorts by version name and skips what is applied, so a gap is harmless and an
out-of-order merge is fine. Two files claiming one number is not. Renumber at review time.

## Write the brief

OpenCode starts cold. It gets the brief plus whatever it reads from the tree, including
`AGENTS.md`. Nothing from the conversation reaches it.

Four blocks carry most tasks:

```
<task>          the job, where it lives, and what to leave untouched
<verification_loop>  the project's real commands, copied from the README
<action_safety> scope limits, and do not commit
<structured_output_contract>  the report to end with
```

Add `<missing_context_gating>` when the task depends on repo facts worth reading rather than
guessing. Point at the requirements section and any spike report that already settled a
choice, so the run follows the decision instead of re-making it.

Name the model explicitly. OpenCode has no default and a bare `run` errors.

```sh
node ~/.claude/skills/opencode-delegate/scripts/relay.mjs \
  --brief brief.txt --model opencode/muse-spark-1.3-contributor-free \
  --cd /path/to/wt/20 --out-dir /path/to/out/20 --timeout 2h
```

## Review what comes back

The run writes `result.json` with its own report. Treat that report as a claim to check.

Re-run the gates yourself. Before believing a linter, prove it scans: break something on
purpose, watch it fire, put it back. A tool matching nothing reports success.

```sh
cargo test --workspace && cargo clippy --workspace --all-targets && cargo fmt --check
# or
go build ./... && go vet ./... && go tool staticcheck ./... && go test ./...
```

Then read the diff against the brief, and run `/ponytail-review` over it for the things that
should not have been written at all.

Things that have actually come up:

- A deviation from the spec dressed as a design decision. Check the requirements text.
- A workaround for a sandbox limit that is not a real limit. A dependency that would not
  resolve offline may resolve fine from the reviewer's shell.
- Dead exports, and doc comments that describe something the code does not do.
- Security defaults turned off with a plausible-sounding reason.

## Fix or send back

Small and isolated, fix it yourself: a dead function, a wrong comment, a flag with a bad
default. Anything touching design, encoding, or several parts of the change goes back:

```sh
node ~/.claude/skills/opencode-delegate/scripts/relay.mjs \
  --brief delta.txt --session ses_... --cd /path/to/wt/20
```

Send only the delta. Say what was right as well as what was wrong, so the good parts survive
the second pass. Answer every open question the run raised, in the brief. A question left
open here becomes a question on the pull request, and a pull request with an open question is
not ready.

## Land it

Commit, push the branch, open the PR. Draft if anything is still undecided, `gh pr ready`
once it is not. Say what changed and why. Prose rules are in `AGENTS.md`.
