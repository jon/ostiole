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
