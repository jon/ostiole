package main

import "testing"

func TestCheckPullRequestAcceptsCompletedTemplate(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body: `## What this does

Adds deterministic policy checks.

## Why

Keep objective rules reproducible.

## Validation

Ran the unit tests.

## Documentation and evidence

Updated the contribution guide; no HIL was run.
`,
	}

	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Fatalf("checkPullRequest() = %#v, want no findings", findings)
	}
}

func TestCheckPullRequestRejectsMalformedTitle(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "feat: add checks",
		Body:  completedPullRequestBody(),
	}
	findings := checkPullRequest(metadata)
	for _, rule := range []string{"subject-case", "subject-period", "subject-prefix"} {
		if !hasFinding(findings, errorLevel, rule) {
			t.Errorf("findings = %#v, want error %q", findings, rule)
		}
	}
}

func TestCheckPullRequestRejectsMissingOrEmptySections(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body: `## What this does

<!-- Describe the change. -->

## Why

A concrete rationale.
`,
	}
	findings := checkPullRequest(metadata)
	if !hasFinding(findings, errorLevel, "pr-body") {
		t.Fatalf("checkPullRequest() = %#v, want pr-body error", findings)
	}
}

func completedPullRequestBody() string {
	return `## What this does

A concrete result.

## Why

A concrete rationale.

## Validation

A concrete test result.

## Documentation and evidence

Documentation remains accurate; no HIL was run.
`
}
