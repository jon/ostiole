package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	markdownhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	xhtml "golang.org/x/net/html"
)

type pullRequestMetadata struct {
	Title string
	Body  string
}

var disallowedHTML = regexp.MustCompile(`(?i)<(/?)(title|textarea|style|xmp|iframe|noembed|noframes|script|plaintext)([\t\n\f\r />])`)

var requiredPullRequestSections = []string{
	"Why",
	"Documentation",
}

func readPullRequestEvent(name string) (pullRequestMetadata, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return pullRequestMetadata{}, err
	}
	var event struct {
		PullRequest struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(content, &event); err != nil {
		return pullRequestMetadata{}, fmt.Errorf("parse pull-request event: %w", err)
	}
	return pullRequestMetadata{
		Title: event.PullRequest.Title,
		Body:  event.PullRequest.Body,
	}, nil
}

func checkPullRequest(metadata pullRequestMetadata) []finding {
	findings := checkSubject("pull-request", strings.TrimSpace(metadata.Title))
	sections := pullRequestSections(metadata.Body)
	if !sections[""] {
		findings = append(findings, newFinding(
			errorLevel, "pr-body", "pull-request",
			"pull-request opening description is missing or empty",
		))
	}
	for _, required := range requiredPullRequestSections {
		if sections["#"+required] {
			continue
		}
		findings = append(findings, newFinding(
			errorLevel, "pr-body", "pull-request",
			fmt.Sprintf("pull-request section %q is missing or empty", required),
		))
	}
	return findings
}

type pullRequestBody struct {
	sections map[string]bool
	current  string
	first    bool
}

func pullRequestSections(body string) map[string]bool {
	result := pullRequestBody{sections: make(map[string]bool), first: true}
	markdown := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote),
		goldmark.WithRendererOptions(markdownhtml.WithUnsafe()),
	)
	source := []byte(body)
	document := markdown.Parser().Parse(text.NewReader(source))
	removeDecorations(document, source)
	var rendered bytes.Buffer
	if err := markdown.Renderer().Render(&rendered, source, document); err != nil {
		return result.sections
	}
	filtered := disallowedHTML.ReplaceAll(rendered.Bytes(), []byte("&lt;$1$2$3"))
	htmlDocument, err := xhtml.Parse(bytes.NewReader(filtered))
	if err != nil {
		return result.sections
	}
	result.addHTMLNode(htmlDocument, true)
	return result.sections
}

func (body *pullRequestBody) heading(level int, title string, sections bool) {
	if sections {
		body.addHeading(level, title)
	} else if body.current == "" && !body.sections[""] {
		body.current = "#"
		body.first = false
	}
}

func (body *pullRequestBody) addHeading(level int, title string) {
	title = strings.TrimSpace(title)
	if body.first && level == 2 && title == "What this does" {
		body.current = ""
	} else if body.current == "" && !body.sections[""] || level <= 2 {
		body.current = "#" + title
	}
	body.first = false
}

func (body *pullRequestBody) addHTMLNode(node *xhtml.Node, sections bool) {
	if node.Type == xhtml.ElementNode {
		if len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6' {
			body.heading(int(node.Data[1]-'0'), htmlText(node), sections)
			return
		}
		switch node.Data {
		case "html", "body", "div", "section", "article", "main":
		default:
			sections = false
		}
	}
	if node.Type == xhtml.TextNode && visibleText(node.Data) {
		body.sections[body.current] = true
		body.first = false
	}
	for _, child := range visibleChildren(node) {
		body.addHTMLNode(child, sections)
	}
}

func htmlText(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var result strings.Builder
	for _, child := range visibleChildren(node) {
		result.WriteString(htmlText(child))
	}
	return result.String()
}

func visibleText(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return r != '\u2800' && !unicode.IsSpace(r) && !unicode.IsControl(r) && !unicode.IsMark(r) &&
			!unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
	})
}

func removeDecorations(node ast.Node, source []byte) {
	if node.Kind() == ast.KindBlockquote {
		removeAlertMarker(node.FirstChild(), source)
	}
	for child := node.FirstChild(); child != nil; {
		next := child.NextSibling()
		switch child.Kind() {
		case extast.KindFootnoteList, extast.KindFootnoteLink:
			node.RemoveChild(node, child)
		default:
			removeDecorations(child, source)
		}
		child = next
	}
}

func removeAlertMarker(node ast.Node, source []byte) {
	paragraph, ok := node.(*ast.Paragraph)
	if !ok || paragraph.Lines().Len() == 0 || paragraph.FirstChild() == nil {
		return
	}
	line := paragraph.Lines().At(0)
	switch strings.TrimSpace(string(line.Value(source))) {
	case "[!NOTE]", "[!TIP]", "[!IMPORTANT]", "[!WARNING]", "[!CAUTION]":
		for child := paragraph.FirstChild(); child != nil && child.Pos() < line.Stop; child = paragraph.FirstChild() {
			paragraph.RemoveChild(paragraph, child)
		}
	}
}

func collapsedDetails(node *xhtml.Node) bool {
	if node.Type != xhtml.ElementNode || node.Data != "details" {
		return false
	}
	for _, attr := range node.Attr {
		if attr.Key == "open" {
			return false
		}
	}
	return true
}

func visibleChildren(node *xhtml.Node) []*xhtml.Node {
	var children []*xhtml.Node
	collapsed := collapsedDetails(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if !collapsed {
			children = append(children, child)
		} else if child.Type == xhtml.ElementNode && child.Data == "summary" {
			return []*xhtml.Node{child}
		}
	}
	return children
}
