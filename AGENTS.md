# AI Agent Guidelines

This file applies to coding agents enhancing Ostiole itself. Before changing
the repository, read and follow [`CONTRIBUTING.md`](CONTRIBUTING.md); its
contribution, style, testing, documentation, commit, and pull-request rules
apply equally to humans and agents.

Agents building programs that consume Ostiole should instead begin with
[`README.md`](README.md) and the user-facing documentation under
[`docs/`](docs/). Repository-maintenance rules do not automatically apply to
downstream compositions.

## Working method

- Start by reading `docs/architecture.md` and `docs/capabilities.md` before
  adding or changing a public package, composition, example, or command.
- Work only within the approved task. Record useful discoveries for later
  rather than implementing unrelated changes.
- Work test-first at every behavioral layer: add a test, run it and observe
  the intended failure, implement the smallest change that makes it pass, and
  then refactor while the test remains green.
- Prefer deterministic behavioral fakes over canned protocol transcripts when
  testing hardware-independent behavior.
- Treat substantial example or command code as evidence that a reusable
  library boundary may be missing. Add and test the smallest appropriate
  public API before composing it into an executable.
- Never bypass an existing library layer by reproducing its USB, adapter,
  wire-protocol, DAP, or target framing in a test or application.
- Keep private plans, donor history, agent activity, and unimplemented
  capabilities out of tracked documentation, commit messages, and pull-request
  prose.

## Commit-size checkpoint

Before forming each commit:

1. Format declarations and calls naturally, then measure the added non-test Go
   in the proposed commit. Do not manipulate formatting to affect the count.
2. If the change exceeds roughly 200 added lines, pause and identify any
   independently testable functionality that could become an earlier commit.
3. Split at a real capability boundary when each resulting commit remains
   coherent, tested, documented, and bisectable.
4. Keep the change together when splitting would separate coupled behavior,
   create artificial seams, or make the history harder to understand. Explain
   that decision in the handoff or pull-request notes.

The threshold is a review prompt, not a quota. Pure movement and formatting do
not justify artificial commits and must not conceal behavioral changes.

## Code Review Rules

### Correctness and ownership

- Report concrete, consequential defects rather than general praise or style
  preferences. Explain the failure mode and point to the narrowest relevant
  code.
- Check behavior, error paths, bounds, timeouts, cancellation, concurrency,
  and cleanup. Trace resource ownership from USB through adapters, wire
  protocols, DAP, targets, examples, and commands.
- Ensure deadlines and cancellation cover blocking host and protocol
  operations without preventing bounded cleanup; cleanup must not depend on
  an operation context that is already canceled.
- Flag leaks, double ownership, discarded primary or cleanup errors, unsafe
  effects, and restoration that cannot be retried. Cleanup that may be retried
  must retain enough state to do so safely.
- Check slice bounds, integer widths, byte order, parity, bit fields, protocol
  state transitions, data races, shared state, and goroutine lifetime at trust
  boundaries.

### Architecture, Go, and tests

- Keep reusable hardware, protocol, lifecycle, restoration, inspection, and
  target behavior in the public package that owns it. Reject duplicated
  lower-layer framing in examples, commands, or tests.
- Require idiomatic, naturally formatted Go beyond mechanical `gofmt`
  acceptance. Prefer conventional local and receiver names and straightforward
  control flow; do not suggest formatting tricks that influence lint or line
  counts.
- Require focused deterministic tests for success, failure, cleanup, and retry
  behavior at the owning package boundary. Keep production packages
  independent of simulators and command-internal policy.
- For integration tests, require the `integration` build tag, skip absent or
  ambiguous hardware before selection, open a selected adapter exactly once,
  gate effectful operations explicitly, and restore volatile state with
  bounded cleanup.

When a change affects a public package, render and review its complete exported
API rather than reading only the diff. Check whether distinct operations can
compare equal, zero values can cause effects, callers repeat owned state,
exported vocabulary remains mutable, fields are ignored in one mode, raw paths
bypass or invalidate state owned by a higher-level value, constructors hide
traffic or cleanup, or bad input reaches hardware before it is rejected.

### Claims and review integrity

- Compare documentation and pull-request claims with the implemented tree.
  Distinguish ordinary tests, behavioral simulation, CI compilation, and
  physical HIL; none is evidence for another.
- Keep architecture ownership, cleanup, safety effects, composition guidance,
  examples, commands, and capability tables consistent with code. For
  Markdown changes, verify relative links, headings, commands, package names,
  examples, and stated limitations against the current tree.
- Require documentation in the same commit as exported API, ownership,
  lifecycle, safety, platform, composition, or validation-claim changes.
- When an API or user-facing capability changes, check whether a compact usage
  example would make the intended interface clearer. Require any example to
  preserve the real ownership, cleanup, and safety rules.
- Keep routine checks which GitHub reports independently out of pull-request
  prose. They remain required publication guards, not evidence to advertise.
- Require physical HIL claims to identify the exercised path and bench.
- When commit context is available, flag unrelated changes, non-bisectable
  commits, weak messages, missing same-commit tests or documentation, and
  artificial splits made only to influence line counts.
- Treat changes to contribution rules, agent review guidance, CODEOWNERS,
  policy tooling, or workflows as security-sensitive review-policy changes.
