package main

import (
	"strings"
	"testing"
)

func TestCheckPullRequestAcceptsCompletedTemplate(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body: `Adds deterministic policy checks.

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
		Body: `Adds deterministic policy checks.

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
	return `A concrete result.

## Why

A concrete rationale.

## Documentation

Documentation remains accurate.
`
}

func TestCheckPullRequestAcceptsLegacyOpeningHeading(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body:  "## What this does\n\n" + completedPullRequestBody(),
	}
	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Fatalf("checkPullRequest() = %#v, want no findings", findings)
	}
}

func TestCheckPullRequestRequiresOpeningDescription(t *testing.T) {
	for _, opening := range []string{"", "<!-- Describe the change. -->\n\n", "## What this does\n\n<!-- Describe the change. -->\n\n"} {
		metadata := pullRequestMetadata{
			Title: "Enforce pull-request policy.",
			Body:  opening + "## Why\n\nA concrete rationale.\n\n## Documentation\n\nDocumentation remains accurate.\n",
		}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Fatalf("opening %q: findings = %#v, want pr-body error", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsHeadingOnlyOpening(t *testing.T) {
	for _, heading := range []string{"# Summary", "### Summary", "#### Summary", "##### Summary", "###### Summary", "   # Summary", "#", "###\tSummary", "Summary\n=======", "Summary\n-------", "Two-line\nsummary\n======="} {
		for _, prefix := range []string{"", "## What this does\n\n"} {
			metadata := pullRequestMetadata{
				Title: "Enforce pull-request policy.",
				Body:  strings.Replace(completedPullRequestBody(), "A concrete result.", prefix+heading, 1),
			}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("heading %q with prefix %q: findings = %#v, want pr-body error", heading, prefix, findings)
			}
		}
	}
}

func TestCheckPullRequestAcceptsNonHeadingHashText(t *testing.T) {
	for _, opening := range []string{"#123 fixes the reported bug.", "####### This is ordinary text.", "A concrete result.\n\n### Details", "A concrete result.\n\nDetails\n-------"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestPreservesBlocksBeforeThematicBreak(t *testing.T) {
	for _, opening := range []string{
		"- Adds deterministic policy checks.\n---",
		"A concrete result.\n- Adds policy checks.\n---",
		"1. Adds deterministic policy checks.\n---",
		"> A concrete result.\n---",
		"    A concrete result.\n---",
		"```text\nA concrete result.\n```\n---",
	} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestDoesNotCountThematicBreaks(t *testing.T) {
	for _, marker := range []string{"- - -", "* * *", "_ _ _", "***", "___", "  *  *  *  ", "-\t-\t-", "_______"} {
		for _, prefix := range []string{"", "## What this does\n\n"} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", prefix+marker, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("marker %q: findings = %#v, want pr-body error", marker, findings)
			}
			metadata.Body = strings.Replace(completedPullRequestBody(), "A concrete result.", prefix+"A concrete result.\n"+marker, 1)
			if findings := checkPullRequest(metadata); len(findings) != 0 {
				t.Errorf("prose before marker %q: findings = %#v", marker, findings)
			}
		}
	}
}

func TestCheckPullRequestRejectsMisplacedLegacyDescription(t *testing.T) {
	for _, body := range []string{
		"## Why\n\nA concrete rationale.\n\n## What this does\n\nA concrete result.\n\n## Documentation\n\nDocumentation remains accurate.",
		"## Documentation\n\nDocumentation remains accurate.\n\n## What this does\n\nA concrete result.\n\n## Why\n\nA concrete rationale.",
		"# Summary\n\n## What this does\n\n" + completedPullRequestBody(),
		"## Why\n\nA concrete rationale.\n\n##\n\nA concrete result.\n\n## Documentation\n\nDocumentation remains accurate.",
	} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: body}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("body %q: findings = %#v, want pr-body error", body, findings)
		}
	}
}

func TestCheckPullRequestRejectsMarkupOnlyOpening(t *testing.T) {
	for _, opening := range []string{">", "> # Summary", "> > ### Summary", "```\n```", "```go\n\n```", "~~~text\n~~~", "-", "- # Summary", "1. ### Summary", "<!-- hidden -->"} {
		for _, prefix := range []string{"", "## What this does\n\n"} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", prefix+opening, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("opening %q: findings = %#v, want pr-body error", opening, findings)
			}
		}
	}
}

func TestCheckPullRequestKeepsSectionMarkersInsideCode(t *testing.T) {
	metadata := pullRequestMetadata{
		Title: "Enforce pull-request policy.",
		Body:  "```markdown\n## Why\nExample, not a section.\n## Documentation\nExample, not a section.\n```",
	}
	if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
		t.Errorf("findings = %#v, want missing sections", findings)
	}
}

func TestCheckPullRequestAcceptsCommentBeforeLegacyOpening(t *testing.T) {
	metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: "<!-- template guidance -->\n\n## What this does\n\n" + completedPullRequestBody()}
	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Errorf("findings = %#v", findings)
	}
}

func TestCheckPullRequestAcceptsVisibleMarkdownContent(t *testing.T) {
	for _, opening := range []string{"**A concrete result.**", "[Description](https://example.com)", "<https://example.com>", "> > A concrete result.", "- **A concrete result.**", "```html\n<!-- visible example -->\n```"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsWhitespaceEntities(t *testing.T) {
	for _, entity := range []string{"&nbsp;", "&#32;", "&#x20;", "**&nbsp;**", "[&#32;](https://example.com)"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, entity, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("entity %q in %q: findings = %#v, want pr-body error", entity, section, findings)
			}
		}
	}
}

func TestCheckPullRequestAcceptsLiteralEntities(t *testing.T) {
	for _, opening := range []string{"`&nbsp;`", "`&#32;`", "\\&nbsp;", "&amp;nbsp;", "&copy;", "```text\n&nbsp;\n```"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsHTMLOpeningHeading(t *testing.T) {
	for _, heading := range []string{"<h1>Summary</h1>", "<H3 class=\"title\">Summary</H3>", "<!-- guidance -->\n<h2>Summary</h2>", "<div>\n<h1>Summary</h1>\n</div>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: heading + "\n\n" + completedPullRequestBody()}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("heading %q: findings = %#v, want pr-body error", heading, findings)
		}
	}
}

func TestCheckPullRequestAcceptsHTMLContentBeforeHeading(t *testing.T) {
	for _, opening := range []string{"<p>A concrete result.</p>", "<div><p>A concrete result.</p><h1>Details</h1></div>", "<!-- <h1>hidden</h1> -->\n\nA concrete result.", "<pre>&lt;h1&gt;literal&lt;/h1&gt;</pre>", "<div title=\"<h1>literal attribute</h1>\">A concrete result.</div>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsEmptyHTMLContent(t *testing.T) {
	for _, opening := range []string{"<div></div>", "<p>&nbsp;</p>", "<h1>Summary</h1><p>A concrete result.</p>", "<div><h1>Summary</h1><p>A concrete result.</p></div>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("opening %q: findings = %#v, want pr-body error", opening, findings)
		}
	}
}

func TestCheckPullRequestAcceptsHTMLSections(t *testing.T) {
	for _, opening := range []string{"<p>A concrete result.</p>", "<h2>What this does</h2><p>A concrete result.</p>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: opening + "<h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p>"}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsLeadingNestedHeading(t *testing.T) {
	for _, opening := range []string{"<table><tr><td><h1>Summary</h1><p>A concrete result.</p></td></tr></table>", "> # Summary\n> A concrete result.", "- # Summary\n\n  A concrete result.", "> > # Summary\n> > A concrete result.", "> <h1>Summary</h1>\n>\n> A concrete result."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("opening %q: findings = %#v, want pr-body error", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsFormatOnlyText(t *testing.T) {
	for _, content := range []string{"&#x200B;", "&shy;", "&#x2060;", "\u200b\u00ad\u2060", "<p>&#x200B;</p>", "`\u200b`"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, content, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("content %q in %q: findings = %#v, want pr-body error", content, section, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesProseBeforeNestedHeading(t *testing.T) {
	for _, opening := range []string{"> A concrete result.\n>\n> # Details", "- A concrete result.\n\n  # Details", "A concrete result.\n\n> # Details\n> More information.", "A\u200b concrete result.", "`&#x200B;`"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestDoesNotPromoteNestedSections(t *testing.T) {
	for _, body := range []string{"A concrete result.\n\n<table><tr><td><h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p></td></tr></table>", "A concrete result.\n\n<blockquote><h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p></blockquote>", "A concrete result.\n\n<ul><li><h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p></li></ul>", "A concrete result.\n\n> ## Why\n> A concrete rationale.\n>\n> ## Documentation\n> Documentation remains accurate.", "A concrete result.\n\n> <h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: body}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("body %q: findings = %#v, want pr-body error", body, findings)
		}
	}
}

func TestCheckPullRequestRejectsEmptyGFMTables(t *testing.T) {
	for _, content := range []string{"| |\n| --- |", "| | |\n| --- | --- |\n| | |", "| &nbsp; |\n| --- |\n| &#x200B; |"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, content, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("table %q in %q: findings = %#v, want pr-body error", content, section, findings)
			}
		}
	}
}

func TestCheckPullRequestAcceptsGFMContent(t *testing.T) {
	for _, opening := range []string{"| Result |\n| --- |\n| A concrete result. |", "| |\n| --- |\n| A concrete result. |", "- [x] A concrete result.", "~~Old behavior~~ A concrete result."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestAcceptsTagFilteredAndUnwrappedHTML(t *testing.T) {
	for _, content := range []string{"<span><script>hidden</script></span>", "<span><style>hidden</style></span>", "<span><template>hidden</template></span>", "<title>hidden</title>"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, content, 1)}
			if findings := checkPullRequest(metadata); len(findings) != 0 {
				t.Errorf("content %q in %q: findings = %#v, want no findings", content, section, findings)
			}
		}
	}
}

func TestCheckPullRequestRejectsLegacyLeadingNestedHeading(t *testing.T) {
	for _, opening := range []string{"<table><tr><td><h1>Summary</h1><p>A concrete result.</p></td></tr></table>", "> # Summary\n> A concrete result.", "- # Summary\n\n  A concrete result.", "<blockquote><h1>Summary</h1><p>A concrete result.</p></blockquote>", "### Summary\n\nA concrete result."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: "## What this does\n\n" + strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("opening %q: findings = %#v, want pr-body error", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsTagFilteredSectionNames(t *testing.T) {
	for _, title := range []string{"Why", "Documentation"} {
		for _, tag := range []string{"script", "style", "title"} {
			heading := "<h2><" + tag + ">" + title + "</" + tag + "></h2>"
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "## "+title, heading, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("heading %q: findings = %#v, want pr-body error", heading, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesVisibleSectionTitles(t *testing.T) {
	metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "## Why", "<h2>Wh<em>y</em><!-- hidden --></h2>", 1)}
	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Errorf("findings = %#v", findings)
	}
}

func TestCheckPullRequestRejectsFootnoteOnlyContent(t *testing.T) {
	for _, content := range []string{"[^note]: A concrete result with scope.", "[^note]\n\n[^note]: A concrete result with scope."} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, content, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("footnote %q in %q: findings = %#v, want pr-body error", content, section, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesDescriptionWithFootnote(t *testing.T) {
	body := strings.Replace(completedPullRequestBody(), "A concrete result.", "A concrete result.[^note]", 1) + "\n[^note]: Supporting detail.\n"
	metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: body}
	if findings := checkPullRequest(metadata); len(findings) != 0 {
		t.Errorf("findings = %#v", findings)
	}
	metadata.Body = strings.Replace(body, "Documentation remains accurate.", "", 1)
	if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
		t.Errorf("findings = %#v, want missing Documentation", findings)
	}
}

func TestCheckPullRequestRejectsIgnorableOnlyText(t *testing.T) {
	for _, content := range []string{"&#xFE0F;", "&#x034F;", "&#x115F;", "&#x3164;", "&#x2800;", "\u0301", "\u0007"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, content, 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("content %q in %q: findings = %#v, want pr-body error", content, section, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesVisibleTextWithMarks(t *testing.T) {
	for _, opening := range []string{"\u2801\u2803", "Cafe\u0301 support.", "日本語の説明。", "✈️ A concrete result."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestRejectsEmptyAlerts(t *testing.T) {
	for _, marker := range []string{"NOTE", "TIP", "IMPORTANT", "WARNING", "CAUTION"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, "> [!"+marker+"]", 1)}
			if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
				t.Errorf("marker %q in %q: findings = %#v, want pr-body error", marker, section, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesAlertContentAndLiteralMarkers(t *testing.T) {
	for _, opening := range []string{"> [!NOTE]\n> A concrete result.", "> [!TIP]\n>\n> A concrete result.", "> ` [!NOTE] `", "> \\[!NOTE]", "[!NOTE]", "> [!NOTE] is a literal marker in this sentence."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestAcceptsDisallowedRawHTMLExamples(t *testing.T) {
	for _, tag := range []string{"title", "textarea", "style", "xmp", "iframe", "noembed", "noframes", "script", "plaintext", "SCRIPT"} {
		for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
			metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, "<"+tag+">Example.</"+tag+">", 1)}
			if findings := checkPullRequest(metadata); len(findings) != 0 {
				t.Errorf("tag %q in %q: findings = %#v", tag, section, findings)
			}
		}
	}
}

func TestCheckPullRequestPreservesRawHTMLRoles(t *testing.T) {
	for _, role := range []string{"doc-endnotes", "doc-noteref"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", "<p role=\""+role+"\">A concrete result.</p>", 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("role %q: findings = %#v", role, findings)
		}
	}
}

func TestCheckPullRequestRejectsCollapsedDetailsContent(t *testing.T) {
	for _, section := range []string{"A concrete result.", "A concrete rationale.", "Documentation remains accurate."} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), section, "<details><p>Hidden detail.</p></details>", 1)}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("section %q: findings = %#v, want pr-body error", section, findings)
		}
	}
	for _, attribute := range []string{"", " open"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: "A concrete result.\n\n<details" + attribute + "><h2>Why</h2><p>Rationale.</p><h2>Documentation</h2><p>Updated.</p></details>"}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("attribute %q: findings = %#v, want pr-body error", attribute, findings)
		}
	}
}

func TestCheckPullRequestPreservesVisibleDetailsContent(t *testing.T) {
	for _, opening := range []string{"<details><summary>A concrete result.</summary><p>Supporting detail.</p></details>", "<details open><p>A concrete result.</p></details>"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: strings.Replace(completedPullRequestBody(), "A concrete result.", opening, 1)}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("opening %q: findings = %#v", opening, findings)
		}
	}
}

func TestCheckPullRequestKeepsSectionsInDocumentFlow(t *testing.T) {
	sections := "<h2>Why</h2><p>A concrete rationale.</p><h2>Documentation</h2><p>Documentation remains accurate.</p>"
	for _, container := range []string{"figure", "aside", "nav", "form", "dl"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: "A concrete result.\n\n<" + container + ">" + sections + "</" + container + ">"}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("container %q: findings = %#v, want pr-body error", container, findings)
		}
	}
	for _, container := range []string{"div", "section", "article", "main"} {
		metadata := pullRequestMetadata{Title: "Enforce pull-request policy.", Body: "A concrete result.\n\n<" + container + ">" + sections + "</" + container + ">"}
		if findings := checkPullRequest(metadata); len(findings) != 0 {
			t.Errorf("container %q: findings = %#v", container, findings)
		}
	}
}
