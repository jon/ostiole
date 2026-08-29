#!/usr/bin/env bash

set -euo pipefail

repository_root=$(git rev-parse --show-toplevel)
filter="${repository_root}/.github/scripts/codex-review-signal.jq"
head_sha=0123456789abcdef0123456789abcdef01234567
pr_number=53
connector='chatgpt-codex-connector[bot]'
temporary_root=$(mktemp -d)
comments_file="${temporary_root}/comments.json"
reactions_file="${temporary_root}/reactions.json"
runs_file="${temporary_root}/runs.json"

cleanup() {
  rm -f "$comments_file" "$reactions_file" "$runs_file"
  rmdir "$temporary_root" 2>/dev/null || true
}
trap cleanup EXIT

fixture() {
  local reviewed_head=$1
  local status=$2
  local reaction_actor=$3
  local reaction_time=$4
  local reviewed_prefix=${reviewed_head:0:7}
  jq -nc \
    --arg reviewed_head "$reviewed_head" \
    --arg reviewed_prefix "$reviewed_prefix" \
    --arg status "$status" \
    --arg reaction_actor "$reaction_actor" \
    --arg reaction_time "$reaction_time" \
    --argjson pr_number "$pr_number" \
    '{
      comments: [{
        user: {login: "chatgpt-codex-connector[bot]"},
        updated_at: "2026-08-29T20:29:40Z",
        body: ("<!-- codex-pull-request-review-summary -->\n<!-- codex-security-review:v1 " + ({headSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", status: "completed"} | tojson) + " -->\n\n| Review | Status | Commit | Review trigger |\n| --- | --- | --- | --- |\n| 📝 **Code Review** | " + (if $status == "completed" then "✅ **Completed** <relative-time></relative-time>" else "🔄 **Running**" end) + " | `" + $reviewed_prefix + "` | Manual request |")
      }],
      reactions: [{
        user: {login: $reaction_actor},
        content: "+1",
        created_at: $reaction_time
      }],
      runs: [{
        head_sha: $reviewed_head,
        created_at: "2026-08-29T20:29:30Z",
        pull_requests: [{number: $pr_number}]
      }]
    }'
}

evaluate_files() {
  jq -e -n \
    --slurpfile comments "$comments_file" \
    --slurpfile reactions "$reactions_file" \
    --slurpfile runs "$runs_file" \
    --arg head "$head_sha" \
    --argjson pr "$pr_number" \
    -f "$filter" >/dev/null
}

evaluate() {
  local input=$1
  jq -c '.comments' <<< "$input" > "$comments_file"
  jq -c '.reactions' <<< "$input" > "$reactions_file"
  jq -c '.runs' <<< "$input" > "$runs_file"
  evaluate_files
}

accept() {
  local name=$1
  local input=$2
  if ! evaluate "$input"; then
    echo "${name}: clean signal was rejected"
    exit 1
  fi
}

reject() {
  local name=$1
  local input=$2
  if evaluate "$input"; then
    echo "${name}: invalid signal was accepted"
    exit 1
  fi
}

matching=$(fixture "$head_sha" completed "$connector" 2026-08-29T20:29:46Z)
accept matching "$matching"
accept reaction-before-summary "$(fixture "$head_sha" completed "$connector" 2026-08-29T20:29:39Z)"
accept later-trigger "$(jq '.runs += [{
  head_sha: .runs[0].head_sha,
  created_at: "2026-08-29T20:40:00Z",
  pull_requests: .runs[0].pull_requests
}]' <<< "$matching")"
reject stale-reaction "$(fixture "$head_sha" completed "$connector" 2026-08-29T20:29:29Z)"
reject simultaneous-reaction "$(fixture "$head_sha" completed "$connector" 2026-08-29T20:29:30Z)"
reject wrong-head "$(fixture ffffffffffffffffffffffffffffffffffffffff completed "$connector" 2026-08-29T20:29:46Z)"
reject wrong-reviewed-head "$(jq '.comments[0].body |= sub("`0123456`"; "`fffffff`")' <<< "$matching")"
reject incomplete "$(fixture "$head_sha" running "$connector" 2026-08-29T20:29:46Z)"
reject wrong-reaction-actor "$(fixture "$head_sha" completed somebody 2026-08-29T20:29:46Z)"
reject wrong-summary-actor "$(jq '.comments[0].user.login = "somebody"' <<< "$matching")"
reject pending-reaction "$(jq '.reactions[0].content = "eyes"' <<< "$matching")"
reject missing-reaction "$(jq '.reactions = []' <<< "$matching")"
reject wrong-summary-kind "$(jq '.comments[0].body |= sub("codex-pull-request-review-summary"; "another-comment")' <<< "$matching")"
reject malformed-summary "$(jq '.comments[0].body = "not metadata"' <<< "$matching")"
reject wrong-run-head "$(jq '.runs[0].head_sha = "ffffffffffffffffffffffffffffffffffffffff"' <<< "$matching")"
reject wrong-run-pr "$(jq '.runs[0].pull_requests[0].number = 54' <<< "$matching")"
reject missing-run "$(jq '.runs = []' <<< "$matching")"

argument_limit=$(getconf ARG_MAX)
if [[ ! "$argument_limit" =~ ^[0-9]+$ ]]; then
  echo "invalid ARG_MAX ${argument_limit}"
  exit 1
fi
fixture_count=$((argument_limit / 40 + 1000))
jq -nc --arg head "$head_sha" --argjson fixture_count "$fixture_count" '
  [range(0; $fixture_count) as $index | {
    user: {login: "somebody"},
    body: ("padding " + ($index | tostring))
  }] + [{
    user: {login: "chatgpt-codex-connector[bot]"},
    updated_at: "2026-08-29T20:29:40Z",
    body: ("<!-- codex-pull-request-review-summary -->\n<!-- codex-security-review:v1 " + ({headSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", status: "completed"} | tojson) + " -->\n\n| Review | Status | Commit | Review trigger |\n| --- | --- | --- | --- |\n| 📝 **Code Review** | ✅ **Completed** <relative-time></relative-time> | `" + ($head[0:7]) + "` | Manual request |")
  }]' > "$comments_file"
jq -nc --arg actor "$connector" '[{user: {login: $actor}, content: "+1", created_at: "2026-08-29T20:29:46Z"}]' > "$reactions_file"
jq -nc --arg head "$head_sha" --argjson pr "$pr_number" '[{
  head_sha: $head,
  created_at: "2026-08-29T20:29:30Z",
  pull_requests: [{number: $pr}]
}]' > "$runs_file"

comments_size=$(wc -c < "$comments_file" | tr -d ' ')
if [[ "$comments_size" -le "$argument_limit" ]]; then
  echo "large fixture is ${comments_size} bytes, want more than ARG_MAX ${argument_limit}"
  exit 1
fi
if ! evaluate_files; then
  echo "large clean signal was rejected"
  exit 1
fi
