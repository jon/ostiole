package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderedMarkdownLinks(t *testing.T) {
	tests := []struct {
		name, content string
		want          []string
	}{
		{"linked image", "[![image](image.png)](guide.md)", []string{"guide.md", "image.png"}},
		{"image alt link", "![alt [label](not-a-link.md)](image.png)", []string{"image.png"}},
		{"nested text", "[text [with brackets]](guide.md)", []string{"guide.md"}},
		{"parenthesized path", "[guide](guide(one).md)", []string{"guide(one).md"}},
		{"wrapped text", "[wrapped\ntext](guide.md)", []string{"guide.md"}},
		{"nested list", "- first\n    - [guide](guide.md)", []string{"guide.md"}},
		{"unmatched backtick", "Unmatched ` followed by [guide](guide.md)", []string{"guide.md"}},
		{"four fences", "````markdown\n```\n[code](ignored.md)\n````\n[guide](guide.md)", []string{"guide.md"}},
		{"long line", strings.Repeat("x", 70000) + "\n\n[guide](guide.md)", []string{"guide.md"}},
		{"comment", "<!-- [ignored](missing.md) -->\n[guide](guide.md)", []string{"guide.md"}},
		{"footnote", "Text[^note].\n\n[^note]: word\n", nil},
		{"footnote link", "Text[^note].\n\n[^note]: [guide](guide.md)\n", []string{"guide.md"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := markdownLinks(test.content); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("links=%#v want %#v", got, test.want)
			}
		})
	}
}

func TestRenderedMarkdownAnchors(t *testing.T) {
	content := "# Use `Read` now\n\n# [Guide](guide.md) and _emphasis_\n\n# A &amp; B <em>here</em>\n\n<!--\n# Hidden comment\n-->\n\n    # Indented code\n\n<div>\n# HTML block\n</div>\n\n> ## Quoted heading\n\nSetext heading\n==============\n"
	got := markdownAnchors(content)
	for _, anchor := range []string{"use-read-now", "guide-and-emphasis", "a--b-here", "quoted-heading", "setext-heading"} {
		if !got[anchor] {
			t.Errorf("missing rendered anchor %q: %#v", anchor, got)
		}
	}
	for _, anchor := range []string{"use--now", "hidden-comment", "indented-code", "html-block"} {
		if got[anchor] {
			t.Errorf("nonheading anchor %q accepted", anchor)
		}
	}
}

func TestRepositoryMarkdownPathsAndFragments(t *testing.T) {
	tests := []struct{ name, source, target, rule string }{
		{"root path", "[guide](/docs/guide.md#known)", "docs/guide.md", ""},
		{"missing root path", "[guide](/missing.md)", "docs/guide.md", "markdown-link"},
		{"root directory", "[root](./)", "docs/guide.md", ""},
		{"percent path", "[guide](percent%25.md)", "percent%.md", ""},
		{"encoded percent path", "[guide](percent%2525.md)", "percent%25.md", ""},
		{"missing percent path", "[guide](missing%25.md)", "docs/guide.md", "markdown-link"},
		{"malformed escape", "[guide](bad%zz.md)", "docs/guide.md", "markdown-link"},
		{"source lines", "[code](code.go#L1-L2)", "code.go", ""},
		{"invalid source anchor", "[code](code.go#made-up)", "code.go", "markdown-anchor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t)
			head := commitPaths(t, repo, "Document paths.\n", map[string]string{"README.md": test.source + "\n", test.target: "# Known\n"})
			findings, err := checkMarkdown(repo, head)
			if err != nil {
				t.Fatal(err)
			}
			if test.rule == "" {
				if len(findings) != 0 {
					t.Fatalf("findings=%#v", findings)
				}
			} else if len(findings) != 1 || !hasFinding(findings, errorLevel, test.rule) {
				t.Fatalf("findings=%#v want one %s", findings, test.rule)
			}
		})
	}
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document parent directory.\n", map[string]string{"docs/guide.md": "[root](../)\n"})
	findings, err := checkMarkdown(repo, head)
	if err != nil || len(findings) != 0 {
		t.Fatalf("parent root=%#v %v", findings, err)
	}
}

func TestInvisibleMetadataDoesNotExplainSections(t *testing.T) {
	for _, hidden := range []string{"<ruby><rp>hidden fallback</rp></ruby>", "$\\phantom{hidden}$", "$$\\phantom{hidden}$$"} {
		metadata := pullRequestMetadata{Title: "Explain the change.", Body: "A concrete result.\n\n## Why\n\n" + hidden + "\n\n## Documentation\n\nThe guide describes it.\n"}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("invisible Why %q accepted: %#v", hidden, findings)
		}
		metadata.Body = strings.Replace(metadata.Body, hidden, "A visible explanation. "+hidden, 1)
		if findings := checkPullRequest(metadata); hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("visible Why rejected: %#v", findings)
		}
	}
}

func TestMarkdownDocumentIsRetainedOnce(t *testing.T) {
	repo := newRepository(t)
	head := commitPaths(t, repo, "Document repeated links.\n", map[string]string{"README.md": "# Known\n\n[one](#known) [two](#known)\n"})
	tree, err := loadMarkdownTree(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	first, err := tree.document("README.md")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tree.document("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("document was reparsed instead of retained")
	}
	if findings, err := tree.checkFile("README.md"); err != nil || len(findings) != 0 {
		t.Fatalf("repeated links=%#v %v", findings, err)
	}
}

func TestMarkdownURLDecodesExactlyOnce(t *testing.T) {
	target, fragment, local, err := resolveMarkdownLink("docs/source.md", "/percent%2525.md#part%2525")
	if err != nil || !local || target != "percent%25.md" || fragment != "part%25" {
		t.Fatalf("URL resolution=%q %q %v %v", target, fragment, local, err)
	}
}

func TestLiteralMathInCodeRemainsVisibleMetadata(t *testing.T) {
	for _, code := range []string{"`$literal code$`", "<pre>$literal code$</pre>", "<code><span>$literal code$</span></code>", "<span>$</span>literal text<span>$</span>"} {
		metadata := pullRequestMetadata{Title: "Explain the change.", Body: "A concrete result.\n\n## Why\n\n" + code + "\n\n## Documentation\n\nThe guide describes it.\n"}
		if findings := checkPullRequest(metadata); hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("visible code %q rejected: %#v", code, findings)
		}
	}
}

func TestExternalURLsStayOutsideLocalPolicy(t *testing.T) {
	for _, destination := range []string{"https://example.invalid/bad%zz", "//example.invalid/bad%zz", "mailto:person@example.invalid"} {
		_, _, local, err := resolveMarkdownLink("README.md", destination)
		if local || err != nil {
			t.Errorf("external %q treated as local: %v %v", destination, local, err)
		}
	}
}

func TestMathFormattingDoesNotExplainSections(t *testing.T) {
	for _, math := range []string{"$`\\phantom{hidden}`$", "$\\phantom{*hidden*}$", "```math\n\\phantom{hidden}\n```", "$$\n\\phantom{hidden}\n$$"} {
		metadata := pullRequestMetadata{Title: "Explain the change.", Body: "A concrete result.\n\n## Why\n\n" + math + "\n\n## Documentation\n\nThe guide describes it.\n"}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("math-only %q accepted: %#v", math, findings)
		}
		metadata.Body = strings.Replace(metadata.Body, math, "A visible explanation.\n\n"+math, 1)
		if findings := checkPullRequest(metadata); hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("visible explanation rejected: %#v", findings)
		}
	}
}

func TestRenderedDestinationEscapes(t *testing.T) {
	for _, test := range []struct{ destination, path string }{
		{"a&amp;b.md", "a&b.md"}, {`a\(b\).md`, "a(b).md"}, {"a&#38;b.md", "a&b.md"}, {"percent%2525.md", "percent%25.md"},
	} {
		repo := newRepository(t)
		head := commitPaths(t, repo, "Document rendered destinations.\n", map[string]string{"README.md": "[guide](" + test.destination + ")\n", test.path: "# Guide\n"})
		if findings, err := checkMarkdown(repo, head); err != nil || len(findings) != 0 {
			t.Errorf("rendered destination %q: %#v %v", test.destination, findings, err)
		}
	}
}

func TestMalformedLocalURLIsReported(t *testing.T) {
	_, _, local, err := resolveMarkdownLink("README.md", "bad%zz.md")
	if !local || err == nil {
		t.Fatalf("malformed local URL: local=%v err=%v", local, err)
	}
}

func TestContainerHeadingsDoNotExplainOpening(t *testing.T) {
	for _, container := range []string{"aside", "figure", "header", "nav", "x-content"} {
		opening := "<" + container + "><h1>Summary</h1></" + container + ">"
		body := opening + "\n\n## Why\n\nA concrete reason.\n\n## Documentation\n\nThe guide describes it.\n"
		metadata := pullRequestMetadata{Title: "Explain the change.", Body: body}
		if findings := checkPullRequest(metadata); !hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("heading-only %q accepted: %#v", container, findings)
		}
		metadata.Body = "A concrete result.\n\n" + body
		if findings := checkPullRequest(metadata); hasFinding(findings, errorLevel, "pr-body") {
			t.Errorf("visible opening before %q rejected: %#v", container, findings)
		}
	}
}
