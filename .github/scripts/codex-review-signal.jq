def reviewed_commit:
  try (
    (.body // "")
    | capture("(?m)^\\| [^|]*\\*\\*Code Review\\*\\* \\| [^|]*\\*\\*Completed\\*\\*[^|]*\\| `(?<prefix>[0-9a-f]{7,40})` \\|").prefix
  ) catch empty;

([$runs[0][]
    | select(.head_sha == $head and any(.pull_requests[]?; .number == $pr))
    | .created_at
  ] | min) as $head_first_seen_at
| select(($head_first_seen_at | type) == "string")
| $comments[0][]
| select(
    .user.login == "chatgpt-codex-connector[bot]"
    and ((.body // "") | startswith("<!-- codex-pull-request-review-summary -->"))
  )
| reviewed_commit as $reviewed_commit
| select($head | startswith($reviewed_commit))
| select(any($reactions[0][];
    .user.login == "chatgpt-codex-connector[bot]"
    and .content == "+1"
    and .created_at > $head_first_seen_at
  ))
| $reviewed_commit
