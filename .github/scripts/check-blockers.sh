#!/usr/bin/env bash
# Fails when a pull request closes an issue that something open still blocks.
#
# GitHub records issue dependencies and shows them on the issue, and it will let you close
# a blocked issue anyway. This is the part that says no. The graph lives on the issues
# themselves, set from the table in #41, so this reads it rather than carrying a copy that
# would drift.
#
# Usage: check-blockers.sh <owner/repo> <pr-number>
set -euo pipefail

repo=${1:?usage: check-blockers.sh <owner/repo> <pr-number>}
pr=${2:?usage: check-blockers.sh <owner/repo> <pr-number>}

closing=$(gh api graphql -f query='
  query($owner: String!, $name: String!, $pr: Int!) {
    repository(owner: $owner, name: $name) {
      pullRequest(number: $pr) {
        closingIssuesReferences(first: 100) { nodes { number } }
      }
    }
  }' -f owner="${repo%%/*}" -f name="${repo##*/}" -F pr="$pr" \
  --jq '.data.repository.pullRequest.closingIssuesReferences.nodes[].number')

if [ -z "$closing" ]; then
  echo "this pull request closes no issues"
  exit 0
fi
echo "closes: $(echo "$closing" | tr '\n' ' ')"

failed=0
for issue in $closing; do
  # An empty answer has to mean "no open blockers" and never "the call failed", so the
  # call is checked before the loop rather than piped into it.
  if ! blockers=$(gh api "repos/$repo/issues/$issue/dependencies/blocked_by" \
                    --jq '.[] | select(.state == "open") | .number'); then
    echo "::error::could not read the dependencies of #$issue"
    exit 1
  fi

  for blocker in $blockers; do
    # One pull request may close a blocker and the thing it blocks in the same go.
    if grep -qx "$blocker" <<<"$closing"; then
      echo "#$issue waits on #$blocker, which this pull request also closes"
      continue
    fi
    echo "::error::#$issue is blocked by #$blocker, which is still open"
    failed=1
  done
done

if [ "$failed" -ne 0 ]; then
  echo "set the dependencies straight or finish the blockers first"
  exit 1
fi
echo "every issue this pull request closes has its blockers closed"
