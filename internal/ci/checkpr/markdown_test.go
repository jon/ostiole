package main

import "testing"

func TestCheckMarkdownAcceptsTrackedLinksAndAnchors(t *testing.T) {
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document the feature.\n", map[string]string{
		"README.md": `# Project

[Guide](docs/guide.md#known-heading)
[Docs](docs)
[Spaced guide](docs/user%20guide.md#spaced-heading)
[Website](https://example.com/not-checked)
`,
		"docs/guide.md":      "# Known heading\n",
		"docs/user guide.md": "# Spaced heading\n",
	})

	findings, err := checkMarkdown(repo, head)
	if err != nil {
		t.Fatalf("checkMarkdown() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("checkMarkdown() = %#v, want no findings", findings)
	}
}

func TestCheckMarkdownRejectsMissingLocalFiles(t *testing.T) {
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document a missing guide.\n", map[string]string{
		"README.md": "[Missing](docs/missing.md)\n",
	})

	findings, err := checkMarkdown(repo, head)
	if err != nil {
		t.Fatalf("checkMarkdown() error = %v", err)
	}
	if !hasFinding(findings, errorLevel, "markdown-link") {
		t.Fatalf("checkMarkdown() = %#v, want markdown-link error", findings)
	}
}

func TestCheckMarkdownRejectsMissingAnchors(t *testing.T) {
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document a missing anchor.\n", map[string]string{
		"README.md":     "[Guide](docs/guide.md#missing-heading)\n",
		"docs/guide.md": "# Known heading\n",
	})

	findings, err := checkMarkdown(repo, head)
	if err != nil {
		t.Fatalf("checkMarkdown() error = %v", err)
	}
	if !hasFinding(findings, errorLevel, "markdown-anchor") {
		t.Fatalf("checkMarkdown() = %#v, want markdown-anchor error", findings)
	}
}

func TestCheckMarkdownIgnoresCodeExamples(t *testing.T) {
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document link syntax.\n", map[string]string{
		"README.md": "```markdown\n[Example](missing.md)\n```\n\n    [Example](missing.md)\n",
	})

	findings, err := checkMarkdown(repo, head)
	if err != nil {
		t.Fatalf("checkMarkdown() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("checkMarkdown() = %#v, want no findings", findings)
	}
}

func TestCheckMarkdownReferenceDestinations(t *testing.T) {
	for _, test := range []struct {
		name       string
		definition string
		wantCode   string
	}{
		{"same line", "[guide]: docs/guide.md#known-heading", ""},
		{"wrapped", "[guide]:\n  docs/guide.md#known-heading", ""},
		{"missing path", "[guide]:\n  docs/missing.md", "markdown-link"},
		{"missing anchor", "[guide]:\n  docs/guide.md#missing-heading", "markdown-anchor"},
		{"angle destination", "[guide]:\n  <docs/missing.md>", "markdown-link"},
		{"external", "[guide]:\n  https://example.com/not-checked", ""},
		{"fenced example", "```markdown\n[guide]:\n  missing.md\n```", ""},
		{"indented example", "    [guide]:\n      missing.md", ""},
		{"blank line", "[guide]:\n\n  missing.md", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t)
			head := commitPaths(t, repo, "Document reference links.\n", map[string]string{
				"README.md":     "[Guide][guide]\n\n" + test.definition + "\n",
				"docs/guide.md": "# Known heading\n",
			})
			findings, err := checkMarkdown(repo, head)
			if err != nil {
				t.Fatalf("checkMarkdown() error = %v", err)
			}
			if test.wantCode == "" {
				if len(findings) != 0 {
					t.Fatalf("checkMarkdown() = %#v, want no findings", findings)
				}
			} else if len(findings) != 1 || !hasFinding(findings, errorLevel, test.wantCode) {
				t.Fatalf("checkMarkdown() = %#v, want one %s error", findings, test.wantCode)
			}
		})
	}
}
