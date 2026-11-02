package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrailerOnlyBodyHasNoExplanation(t *testing.T) {
	for _, body := range []string{
		"Signed-off-by: Example Person <person@example.com>",
		"Reviewed-by: Another Person <reviewer@example.com>\nSigned-off-by: Example Person <person@example.com>\n",
		"Custom-token: detail\n continued detail\n\n",
	} {
		summary := changeSummary{changedLines: 20}
		findings := changeFindings(summary, false, "abc", "Explain the change.\n\n"+body)
		if !hasFinding(findings, warningLevel, "commit-body") {
			t.Errorf("trailer-only body %q has no warning: %#v", body, findings)
		}
		if !hasCommitBody("Explain the change.\n\nA real explanation.\n\n" + body) {
			t.Errorf("explanation before %q was lost", body)
		}
	}
}

func TestBodyWrapExemptionsReachWrappableLines(t *testing.T) {
	prose := strings.Repeat("long words ", 10) + "end"
	tests := []struct{ name, body string }{
		{"URL", "https://example.com/guide " + prose},
		{"indent", "    " + prose},
		{"tab", "\t" + prose},
		{"code span", "`" + prose + "`"},
		{"double code span", "``git show `revision` " + prose + "``"},
		{"longer code span", "```git show ``revision`` " + prose + "```"},
		{"prompt", "$ git show " + prose},
		{"trailer", "A real explanation.\n\nSigned-off-by: " + prose},
		{"backtick fence", "```text\n" + prose + "\n```"},
		{"tilde fence", "~~~~text\n" + prose + "\n~~~~~"},
		{"embedded shorter fence", "````text\n```\n" + prose + "\n````"},
		{"embedded other fence", "```text\n~~~\n" + prose + "\n```"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reached := false
			for _, line := range strings.Split(test.body, "\n") {
				reached = reached || utf8.RuneCountInString(line) > 72 && strings.Contains(line, " ")
			}
			if !reached {
				t.Fatal("fixture does not reach wrap guard")
			}
			findings := checkMessage("abc", "Explain the change.\n\n"+test.body+"\n")
			if hasFinding(findings, errorLevel, "body-length") {
				t.Fatalf("exempt body rejected: %#v", findings)
			}
		})
	}
}

func TestBodyWrapRejectsFalseExemptions(t *testing.T) {
	prose := strings.Repeat("long words ", 10) + "end"
	bodies := []string{
		"Reason: " + prose + "\n\nAnother explanatory paragraph.",
		"Reason: " + prose + "\nAn ordinary continuation.",
		"```text\n" + prose,
		"````text\n```\n" + prose + "\n```",
		"~~~text\n```\n" + prose + "\n```",
		"```text\n" + prose + "\n~~~",
		"````text\n" + prose + "\n```",
		"```text\nshort\n``` trailing prose\n" + prose,
		"`first code` " + prose + " `last code`",
	}
	for _, prefix := range []string{"make ", "go ", "test ", "git ", "clang ", "curl ", "docker ", "gh ", "golangci-lint ", "npm ", "npx ", "staticcheck ", "xcrun ", "./"} {
		bodies = append(bodies, prefix+prose)
	}
	for _, body := range bodies {
		findings := checkMessage("abc", "Explain the change.\n\n"+body+"\n")
		if !hasFinding(findings, errorLevel, "body-length") {
			t.Errorf("wrappable body accepted: %q; %#v", body, findings)
		}
	}
}

func TestAnnotationPropertiesAndDataEscapeSeparately(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	var output bytes.Buffer
	writeFinding(&output, newFinding(errorLevel, "rule:,\r\n%", "abc", "detail: one, two %\r\nnext"))
	want := "::error title=rule%3A%2C%0D%0A%25 in abc::detail: one, two %25%0D%0Anext\n"
	if output.String() != want {
		t.Fatalf("annotation = %q, want %q", output.String(), want)
	}
}
