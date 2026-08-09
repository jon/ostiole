---
applyTo: ".github/**/*.yml,.github/**/*.yaml"
---

# GitHub Actions review rules

- Grant the smallest explicit token permissions and pin every external Action
  to a full commit SHA.
- Never expose repository secrets or write-capable tokens to untrusted pull
  request code.
- A `pull_request_target` workflow must not check out, source, evaluate, or
  execute the pull request head or interpolate untrusted text into commands.
- Required checks must run for every relevant pull request and must not pass
  merely because a path, job, or workflow was skipped.
- Keep HIL and effectful hardware operations off untrusted hosted runners.
- Preserve supported Linux, macOS arm64, and macOS Intel validation and use
  stable check names suitable for branch rulesets.
