package main

import (
	"strings"
	"testing"
)

func TestCheckMessageAcceptsRepositoryStyle(t *testing.T) {
	message := `Check contribution messages deterministically.

Explain why the policy belongs in a reproducible check and how future
contributors receive useful feedback without replacing a maintainer's
review.
`

	if findings := checkMessage("abc123", message); len(findings) != 0 {
		t.Fatalf("checkMessage() = %#v, want no findings", findings)
	}
}

func TestCheckMessageRejectsMalformedSubjects(t *testing.T) {
	tests := []struct {
		name    string
		message string
		rule    string
	}{
		{name: "lowercase", message: "check the policy.\n", rule: "subject-case"},
		{name: "no period", message: "Check the policy\n", rule: "subject-period"},
		{name: "conventional", message: "fix: Check the policy.\n", rule: "subject-prefix"},
		{name: "scoped conventional", message: "ci(check): Check the policy.\n", rule: "subject-prefix"},
		{name: "long", message: strings.Repeat("A", 120) + ".\n", rule: "subject-length"},
		{name: "missing separator", message: "Check the policy.\nBody without a separator.\n", rule: "body-separator"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings := checkMessage("abc123", test.message)
			if !hasFinding(findings, errorLevel, test.rule) {
				t.Fatalf("checkMessage() = %#v, want error %q", findings, test.rule)
			}
		})
	}
}

func TestCheckMessageWarnsBetweenPreferredAndMaximumLength(t *testing.T) {
	for _, length := range []int{73, 120} {
		subject := "A" + strings.Repeat("b", length-2) + "."
		findings := checkMessage("abc123", subject+"\n")
		if !hasFinding(findings, warningLevel, "subject-length") {
			t.Fatalf("length %d findings = %#v, want warning", length, findings)
		}
		if hasLevel(findings, errorLevel) {
			t.Fatalf("length %d findings = %#v, want no error", length, findings)
		}
	}
}

func TestCheckMessageRejectsClearlyWrappableBodyProse(t *testing.T) {
	message := "Check the policy.\n\n" + strings.Repeat("word ", 16) + "word\n"
	findings := checkMessage("abc123", message)
	if !hasFinding(findings, errorLevel, "body-length") {
		t.Fatalf("checkMessage() = %#v, want body-length error", findings)
	}
}

func TestCheckMessageAllowsUnwrappableBodyLines(t *testing.T) {
	long := strings.Repeat("x", 90)
	tests := []string{
		"https://example.com/" + long,
		"    " + long,
		"`" + long + "`",
		"git show --format=" + long,
		"Signed-off-by: Example Person <person@example.com>",
	}

	for _, line := range tests {
		message := "Check the policy.\n\n" + line + "\n"
		if findings := checkMessage("abc123", message); hasFinding(findings, errorLevel, "body-length") {
			t.Errorf("line %q produced findings %#v", line, findings)
		}
	}

	fenced := "Check the policy.\n\n```text\n" + long + " with prose\n```\n"
	if findings := checkMessage("abc123", fenced); hasFinding(findings, errorLevel, "body-length") {
		t.Fatalf("fenced code produced findings %#v", findings)
	}
}

func TestCheckMessageWarnsAboutObviousNonImperativeSubjects(t *testing.T) {
	for _, subject := range []string{
		"Added the policy checker.",
		"Adding the policy checker.",
		"This adds the policy checker.",
	} {
		findings := checkMessage("abc123", subject+"\n")
		if !hasFinding(findings, warningLevel, "subject-mood") {
			t.Errorf("subject %q produced findings %#v", subject, findings)
		}
	}
}

func hasFinding(findings []finding, level severity, rule string) bool {
	for _, finding := range findings {
		if finding.level == level && finding.rule == rule {
			return true
		}
	}
	return false
}

func hasLevel(findings []finding, level severity) bool {
	for _, finding := range findings {
		if finding.level == level {
			return true
		}
	}
	return false
}
