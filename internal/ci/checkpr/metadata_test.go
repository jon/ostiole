package main

import "testing"

func TestCheckPullRequestAcceptsCompletedTemplate(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body: `## What this does

Adds deterministic policy checks.

## Why

Objective pull-request rules should produce the same result locally and on GitHub.

## Documentation

Updated the contribution guide.
`,
	}

	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Fatalf("checkPullRequest() = %#v, want no findings", findings)
	}
}

func TestCheckPullRequestAcceptsOptionalHardwareEvidence(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body: `## What this does

Adds deterministic policy checks.

## Why

Objective pull-request rules should produce the same result locally and on GitHub.

## Hardware evidence

On the FT232H bench, the target returned the expected DPIDR and the channel closed without error.

## Documentation

Updated the contribution guide.
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

## Documentation

Documentation remains accurate.
`
}
