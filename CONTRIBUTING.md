# Contributing to Ostiole

Ostiole welcomes contributions from people and coding agents. Both follow the
same repository standards. This document covers changes to Ostiole itself;
programs that consume the library should begin with [`README.md`](README.md)
and the user guides under [`docs/`](docs/).

Participation is also subject to the short, common-sense
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md). Report suspected vulnerabilities
privately as described in [`SECURITY.md`](SECURITY.md), not in a public issue.

Ostiole is an experimental hardware-access library. Small changes can affect
USB ownership, protocol state, and a connected target, so contributions should
be narrow, explicit, and tested at the layer that owns the behavior.

## Acceptance criteria

Before acceptance, a pull request must:

- tell one coherent story without unrelated changes;
- include appropriate tests for its behavior and regression surface;
- keep public documentation and capability claims consistent with the code;
- preserve supported Linux and macOS behavior, or introduce a new host with
  its implementation, tests, CI coverage, and documentation;
- use idiomatic, maintainable code at the layer that owns the behavior; and
- pass the applicable software, simulation, and hardware validation described
  below without overstating what was exercised.

## Change and commit shape

- Prefer small commits that introduce one coherent capability together with
  its tests and applicable documentation.
- Treat roughly 200 added lines of non-test Go as a signal to consider whether
  a commit has a natural, independently useful split. It is a guideline, not
  an acceptance limit; keep tightly coupled behavior together when splitting
  it would make the history less clear.
- Pure movement and formatting do not require artificial splits, but must not
  conceal functional changes.
- Keep production functions within the repository lint limits: cognitive
  complexity 20, cyclomatic complexity 12, 60 lines, 40 statements, and
  nesting depth 5.

## Go style

- Write idiomatic Go and run `gofmt`. Formatting mechanically accepted by
  `gofmt` is not automatically readable.
- Keep ordinary declarations, signatures, and calls compact when they fit
  naturally. Break lines to clarify structure, not to place every parameter
  or argument on its own line or to influence a line-count limit.
- Use conventional short receiver names and names proportional to their
  scope. Do not lengthen familiar local names merely to make them descriptive
  in isolation.
- Use normal multiline composite literals when a literal no longer reads
  clearly on one line.
- Prefer straightforward control flow. If a multiline call makes an `if`
  initializer awkward, assign its result first and test it separately.
- Keep reusable hardware, protocol, lifecycle, restoration, inspection, and
  target behavior in the appropriate public package.
- Keep `cmd/<name>/main.go` thin. Commands own arguments, user-facing
  selection and defaults, output, exit status, and composition—not a parallel
  hardware stack.
- Keep examples compact demonstrations of public APIs. Do not duplicate
  framing already owned by a library package.

## Public API design

- Expose the meaning owned by a package, not an incidental encoding from the
  layer below it. Distinct operations remain distinct even when they share a
  wire value.
- Put every fact needed to interpret a value in its type or owning object.
  This includes direction, bank, response mode, device or register class, and
  resource ownership.
- Prefer immutable constants and opaque constructed values for public
  vocabulary. Exported sentinel errors are the ordinary exception.
- Make zero values harmless or invalid. A zero value must never silently
  select an effectful operation.
- Do not ask callers to repeat state which the callee already owns or can
  derive. State transitions which must succeed together belong to one owning
  operation.
- Keep a raw escape hatch only at the layer which owns its representation. It
  must not bypass cached or restorable state owned by a higher-level value.
- Name acquisition, effects, restoration, and cleanup plainly. Do not hide
  target traffic or cleanup obligations behind a constructor which appears to
  allocate a value.
- Do not export an enum or abstraction with one concrete choice in case a
  second choice appears later.
- Reject invalid input before USB, adapter, wire, debug-port, access-port, or
  target traffic.
- Before v1, replace a misleading API instead of preserving parallel old and
  new vocabularies through aliases or compatibility shims.

## Tests and hardware safety

- Include behavioral tests at the package boundary that owns the change. Keep
  hardware-independent tests deterministic and prefer behavioral fakes over
  canned byte transcripts.
- Build examples directly. Do not add `_test.go` files merely to test example
  formatting or create artificial seams in a `main` package. Add an
  example-local test only for meaningful behavior the example itself owns.
- Put tests requiring physical hardware behind the `integration` build tag.
- Treat absent or ambiguous bench hardware as a skip. After a test selects an
  adapter, open it exactly once; contention and every other open failure are
  failures.
- Keep ordinary identity tests read-only. Explicitly gate reset, halt, target
  writes, and other effectful operations, and restore volatile target state
  before returning.
- Do not infer hardware support from simulation or CI. Record simulation,
  compilation, and physical validation as distinct evidence.

Run the applicable software checks before presenting a contribution:

```sh
test -z "$(gofmt -l .)"
go build ./...
go vet ./...
go test -race ./...
staticcheck ./...
golangci-lint run --config .golangci.yml ./...
golangci-lint run --config .golangci.tests.yml ./...
```

Pull requests divide automated feedback into deterministic failures and
review judgments. The `policy` check rejects merge commits, malformed commit
subjects, Conventional Commit prefixes, subjects longer than 120 columns,
missing body separators, clearly wrappable body prose longer than 72 columns,
incomplete pull-request metadata, and broken local Markdown links or anchors.
The `commits` check runs formatting, build, vet, and race tests independently
at every commit. The `quality` and macOS checks validate the final tip with
the additional linters, vulnerability scan, integration-tag compilation, and
native C checks applicable to their hosts. The `CodeQL` workflow analyzes Go
and native C/C++ with the security-extended query suite on every pull request,
including pull requests from forks.

Policy annotations remain advisory when judgment is required. These include
73- through 120-column subjects, ambiguous imperative mood, weak bodies,
commits near the 200-line review checkpoint, likely missing tests or docs,
mixed capabilities, and changes to review-policy files. Codex and the
maintainer assess correctness, ownership, cleanup, hardware safety,
architecture, behavioral coverage, and documentation claims. A completed
standard Codex review must match the current pull-request head. Codex must
reject a nontrivial commit whose message does not explain why the commit is
needed and what behavior it establishes at that boundary. Codex must also
reject a pull request which adds an API without representative calls in “What
this does,” or changes an API without representative calls before and after
the change. It must also reject examples which hide ownership, cleanup, safety,
or migration details needed to understand ordinary use, and prose paragraphs
which are hard-wrapped in the Markdown source. On the final head, comment
`@codex review`; a clean review may instead complete with a SHA-labeled Codex
result comment. Automatic review begins when a pull request becomes ready, and
the connector reacts to the pull request with a thumbs-up after a clean review.
The gate accepts a SHA-labeled clean result, or a completed Code Review row
whose displayed commit resolves to the exact head together with the
connector's current thumbs-up. After a head change, the thumbs-up must also be
created after the earliest GitHub Actions run for that pull request and head.
A formal review with suggestions does not satisfy the gate. A Codex opinion
does not count as an approval. Resolve its actionable conversations or explain
the disposition before merging.
If Codex completes after the gate's polling window, rerun the failed
`codex-reviewed` job to evaluate the completed result.

Untrusted pull-request workflows receive no secrets or write-capable checkout
credentials and never run HIL. CodeQL uses the ordinary `pull_request` event
and GitHub's built-in token; it does not use `pull_request_target` or a
maintainer credential.

When changing the native macOS USB bridge, also verify its formatting and
warning-clean C build with the commands used by `.github/workflows/test.yml`.
Run the relevant opt-in HIL tests when the contribution changes an exercised
hardware path.

## Pull-request descriptions

Use “What this does” for the resulting behavior and scope. For every new API,
show representative calls which make ordinary use concrete. For every changed
API, show representative calls before and after the change so that the
migration is visible. The examples must preserve the same ownership, cleanup,
and safety rules as ordinary code.

Leave each prose paragraph on one line in the Markdown source and let GitHub
wrap it for display. Do not insert source line breaks merely to meet a column
limit. Format lists and code blocks according to their own structure.

Use “Why” for the old constraint, the reason for the change, and any design
choice that is not evident from the result. Use “Documentation” to name the
public material changed or explain why the existing documentation remains
enough.

“Hardware evidence” is optional. Include it for physical HIL, a manual bench
experiment, or other evidence which GitHub cannot reproduce. Name the command
or procedure, the bench, what happened, whether state was restored, and what
the result does not establish. Do not list or claim routine format, build,
test, lint, policy, vulnerability, or compilation checks which GitHub reports
independently.

## Documentation

Every pull request must leave the public documentation consistent with the
code. A pull request that does not change public behavior may need no
documentation edit, but its author must still verify that the existing claims
remain true.

Update the relevant documentation in the same commit when changing:

- an exported API or package responsibility;
- an ownership, cleanup, or lifecycle rule;
- a safety effect;
- supported hardware or host platforms;
- an example, command, or recommended composition path; or
- a simulation, CI, or physical-validation claim.

Keep `docs/composition.md` aligned with the highest reusable public layer, and
describe only behavior present in that commit. Do not publish private plans,
donor implementation history, prospective package names, or publication
sequencing as user documentation.

## Commit messages

Ostiole does not use Conventional Commits. The first line must be a single,
complete sentence describing the resulting change:

- begin with a capital letter;
- end with a period;
- use the imperative mood where it reads naturally;
- omit category and scope prefixes such as `feat:`, `fix:`, or `usb:`; and
- prefer 72 columns or fewer. CI warns at 73 through 120 columns and rejects
  longer subjects.

Examples:

- `Decode structural USB descriptors.`
- `Release debug-port power ownership.`

After the summary, leave a blank line. Truly trivial commits may stop there.
Otherwise, use a short body which lets someone reviewing that commit
understand why it belongs in the history. Name the previous limitation or
failure, say what a caller or maintainer can rely on afterward, and explain
any important design or safety choice which is not evident from the diff.
Include evidence when it supports a claim; a list of tests does not substitute
for the reason for the change.

Write about the state before and after that commit, not the eventual pull
request or the process used to produce it. Do not merely enumerate files,
restate the summary, narrate implementation steps, or recount review and
construction history.

“Add logical registers, tests, and documentation” makes the reader infer the
problem. A useful body says what the change buys:

```text
DP register pairs share physical SWD addresses, so a raw address does
not tell callers which register they will access. Give each register a
logical identity and keep direction and bank selection inside DebugPort.
```

Wrap prose in stored commit bodies at 72 columns. Leave URLs, command lines,
code blocks, and other text that cannot be broken safely intact. Keep messages
timeless and omit internal planning, construction history, and agent activity.

## History and pull requests

- Keep the commit series linear, incremental, and bisectable. Do not add merge
  commits.
- Each pull request should tell one coherent functional story and contain only
  commits that belong to that story.
- Treat commits on an open pull-request branch as working review history.
  Rewrite them when doing so folds a correction into the commit that
  introduced it, restores atomicity, or makes the final sequence easier to
  understand. Review feedback does not require a permanent fixup commit.
- Before rewriting a pull-request branch, fetch it and confirm that nobody
  else has advanced it. Update the remote with `git push --force-with-lease`,
  never an unconditional force push, and coordinate with anyone building on
  that branch.
- Once a commit has been merged into `main` or included in a release tag,
  treat it as immutable. Never rewrite or force-push merged branches or tags.
- Make the first pull-request push coherent and ready for review, then refine
  its history deliberately as review reveals necessary changes. Avoid
  gratuitous churn that invalidates review context without improving the
  series.

Before requesting review, confirm that every commit builds and tests on its
own, the final worktree is clean, the documentation matches the code, and the
pull-request description reports any evidence GitHub cannot reproduce without
overstating support.
