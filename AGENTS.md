# AI Agent Guidelines

Ostiole is routinely developed with coding agents. Treat this file as the
repository-specific contribution contract for agents enhancing Ostiole
itself.

This is not a guide for programs that consume Ostiole as a library. Agents
building such programs should begin with `README.md` and the user-facing
documentation under `docs/` when present. Repository workflow, history, and
maintainer constraints in this file do not automatically apply to downstream
compositions.

## Workflow

- Commit changes incrementally with clear, atomic messages.
- Use test-driven development at every layer: write the failing test, observe
  it fail, then implement only enough behavior to make it pass.
- Keep hardware-independent tests deterministic and prefer behavioral fakes
  over canned byte transcripts.
- Do not find or implement unrelated work while carrying out an approved task.
- Linux is the only supported host until another platform is introduced
  explicitly with its implementation and tests.

## Commit messages

Ostiole does not use Conventional Commits.

The first line of every commit message must be a single, complete sentence
describing what the commit does:

- Begin with a capital letter.
- End with a period.
- Use the imperative mood where it reads naturally.
- Do not add a scope or category prefix such as `feat:`, `fix:`, `ci:`, or
  `usb:`.
- Keep the sentence at 72 columns or fewer so it remains legible in an
  abbreviated log.
- Describe the resulting change, not the process of creating it.

Examples:

- `Establish the licensed Linux Go module.`
- `Read a generic SWD DPIDR from Linux.`
- `Decode structural USB descriptors.`
- `Release debug-port power ownership.`

After the summary, leave a blank line before any commit body.

Wrap prose paragraphs in stored commit bodies at 72 columns. Reflow the text
before committing rather than relying on a terminal or web interface. Leave
URLs, command lines, code blocks, trailers, and other text that cannot be
broken safely intact.

Truly trivial commits may omit the body. Every other commit should include a
short body explaining information that is not evident from the diff itself.
Useful subjects include:

- why the change was necessary;
- what behavior or constraint existed before the change;
- what becomes possible or reliable afterward;
- important design or safety decisions;
- how the behavior was tested, including relevant simulation or hardware
  validation.

Do not merely enumerate changed files or restate the summary. Keep the message
timeless and focused on the commit itself. Do not include internal planning,
construction history, agent activity, or discussion that is irrelevant to a
future maintainer.

## Published history

- Rewrite only commits that have never appeared on a public remote, tag, or
  pull request.
- A commit is permanently published once exposed publicly, even if its branch
  is later deleted.
- Never force-push a public branch or tag.
- Before rewriting, fetch public remotes and prove that the candidate commits
  are not reachable from any public ref. Stop if their status is uncertain.
- Prepare public branches and pull-request prose locally. Their first public
  push must already be final.

## Commit shape

- Except for the initial hardware proof, add no more than 200 lines of
  non-test Go in one commit.
- Format declarations and calls naturally before measuring that limit. Do not
  put every parameter or argument on its own line merely for symmetry or to
  influence the line count.
- Pure code movement and formatting do not need to be split to satisfy the
  line limit, but must not conceal functional changes.
- Introduce exactly one capability per commit, together with its tests and
  documentation.
- Keep production functions within cognitive complexity 20, cyclomatic
  complexity 12, 60 lines, 40 statements, and nesting depth 5.

## Hardware tests

- Put tests that require a physical adapter behind the `integration` build
  tag.
- Treat absent or ambiguous bench hardware as a skip, not a failure.
- Once a test selects an adapter, open it exactly once. Contention and all
  other open failures are test failures.
- Keep ordinary identity tests read-only. Gate reset, halt, and memory writes
  explicitly, and restore volatile target state before returning.

## Executable examples and commands

- Keep examples compact and readable as demonstrations of public APIs. Do not
  add `_test.go` files merely to test example formatting or create artificial
  seams; build examples and run their exact binaries on HIL when applicable.
- Add an example-local test only for meaningful behavior owned by the example
  and not already covered at a library boundary.
- Keep `cmd/<name>/main.go` thin. Put substantive parsing, dispatch, session,
  and presentation behavior in a testable package rather than avoiding tests
  for executable functionality altogether.

## Command boundaries

- Keep commands minimal. They own argument parsing, user-facing selection and
  defaults, output formatting, exit status, and composition of public library
  APIs.
- Put reusable hardware, protocol, lifecycle, restoration, inspection, and
  target semantics in the appropriate public library package. Do not hide
  these behaviors in `cmd/ost` or its internal packages.
- Treat substantial command implementation as evidence of a missing library
  boundary. Add and test the smallest reusable API first, then make the
  command call it.
- Use command-internal packages only for application policy and testable
  process orchestration. They must not become a second, command-only hardware
  stack.
- Expose safe semantic operations rather than mirroring every low-level
  mechanism as a command. A public library primitive does not automatically
  require a corresponding CLI surface.

## Library boundaries

- Build diagnostics and examples through the same composable public APIs
  offered to users.
- Keep raw USB, adapter, and wire-protocol framing inside their corresponding
  packages once those packages exist.
- Fix protocol or performance defects in the library rather than bypassing it
  in an example or test.
