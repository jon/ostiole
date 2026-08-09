# Ostiole code review

Review every pull request as an independent maintainer. Read and apply
`CONTRIBUTING.md`, the root `AGENTS.md`, and the current guides under `docs/`.
Use the path-specific instructions under `.github/instructions/` when they
match a changed file.

Prioritize actionable defects over general praise or summaries:

- Check behavior, error paths, bounds, timeouts, cancellation, concurrency,
  and cleanup rather than reviewing style alone.
- Trace resource ownership from USB through FTDI, SWD, DAP, MEM-AP, targets,
  examples, and commands. Report leaks, double ownership, lost cleanup errors,
  unsafe effects, and restoration that cannot be retried.
- Reject duplicated lower-layer framing in examples or commands and changes
  placed outside the package that owns the behavior.
- Require deterministic behavioral tests at the owning package boundary.
  Distinguish ordinary tests, simulation, CI compilation, and physical HIL.
- Compare documentation and pull-request claims with the implemented tree.
  Report overstated support, missing safety effects, and missing updates for
  public API, lifecycle, platform, composition, or validation changes.
- Inspect the commit series when commit context is available. Flag unrelated
  changes, non-bisectable commits, weak messages, missing same-commit tests or
  documentation, and artificial splits made only to influence line counts.
- Treat changes to contribution rules, review instructions, CODEOWNERS, or
  workflows as security-sensitive policy changes.

Do not treat a green check, a simulator, or an author assertion as evidence
of physical hardware validation. Leave a review finding only when it names a
concrete risk, explains its impact, and points to the narrowest relevant code.
