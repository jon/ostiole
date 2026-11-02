package main

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	markdownhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	xhtml "golang.org/x/net/html"
)

var linkScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

var sourceLineAnchor = regexp.MustCompile(`^l[0-9]+(-l[0-9]+)?$`)

type markdownTree struct {
	repo          string
	head          string
	files         map[string]bool
	directories   map[string]bool
	markdownFiles []string
	documents     map[string]*markdownDocument
}

func checkMarkdown(repo, head string) ([]finding, error) {
	tree, err := loadMarkdownTree(repo, head)
	if err != nil {
		return nil, err
	}
	var findings []finding
	for _, source := range tree.markdownFiles {
		fileFindings, err := tree.checkFile(source)
		if err != nil {
			return nil, err
		}
		findings = append(findings, fileFindings...)
	}
	return findings, nil
}

func loadMarkdownTree(repo, head string) (*markdownTree, error) {
	output, err := gitOutput(repo, "ls-tree", "-r", "--name-only", "-z", head)
	if err != nil {
		return nil, err
	}
	tree := &markdownTree{
		repo:        repo,
		head:        head,
		files:       make(map[string]bool),
		directories: map[string]bool{".": true},
		documents:   make(map[string]*markdownDocument),
	}
	for _, entry := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
		name := string(entry)
		tree.files[name] = true
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			tree.directories[directory] = true
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			tree.markdownFiles = append(tree.markdownFiles, name)
		}
	}
	return tree, nil
}

func (tree *markdownTree) document(name string) (*markdownDocument, error) {
	if document, ok := tree.documents[name]; ok {
		return document, nil
	}
	content, _, err := readGitFile(tree.repo, tree.head, name)
	if err != nil {
		return nil, err
	}
	document := parseMarkdown(string(content))
	tree.documents[name] = document
	return document, nil
}

func (tree *markdownTree) checkFile(source string) ([]finding, error) {
	document, err := tree.document(source)
	if err != nil {
		return nil, err
	}
	var findings []finding
	for _, link := range document.links() {
		linkFinding, err := tree.checkLink(source, link)
		if err != nil {
			return nil, err
		}
		if linkFinding != nil {
			findings = append(findings, *linkFinding)
		}
	}
	return findings, nil
}

func (tree *markdownTree) checkLink(source, link string) (*finding, error) {
	target, fragment, local, parseErr := resolveMarkdownLink(source, link)
	if parseErr != nil {
		result := newFinding(errorLevel, "markdown-link", tree.head, fmt.Sprintf("%s has invalid link %q: %v", source, link, parseErr))
		return &result, nil
	}
	if !local {
		return nil, nil
	}
	if target != "" && !tree.files[target] && !tree.directories[target] {
		result := newFinding(
			errorLevel, "markdown-link", tree.head,
			fmt.Sprintf("%s links to missing local path %q", source, target),
		)
		return &result, nil
	}
	if fragment == "" || target == "" || tree.directories[target] {
		return nil, nil
	}
	exists, err := tree.anchorExists(target, fragment)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	result := newFinding(
		errorLevel, "markdown-anchor", tree.head,
		fmt.Sprintf("%s links to missing anchor %q in %s", source, fragment, target),
	)
	return &result, nil
}

func (tree *markdownTree) anchorExists(target, fragment string) (bool, error) {
	if !strings.HasSuffix(strings.ToLower(target), ".md") {
		return sourceLineAnchor.MatchString(fragment), nil
	}
	document, err := tree.document(target)
	if err != nil {
		return false, err
	}
	return document.anchors()[fragment], nil
}

type markdownDocument struct {
	source    []byte
	root      ast.Node
	markdown  goldmark.Markdown
	anchorSet map[string]bool
}

func parseMarkdown(content string) *markdownDocument {
	markdown := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote), goldmark.WithRendererOptions(markdownhtml.WithUnsafe()))
	source := []byte(content)
	return &markdownDocument{source: source, root: markdown.Parser().Parse(text.NewReader(source)), markdown: markdown}
}

func markdownLinks(content string) []string { return parseMarkdown(content).links() }

func (document *markdownDocument) links() []string {
	var links []string
	_ = ast.Walk(document.root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch node := node.(type) {
			case *ast.Link:
				links = append(links, string(util.URLEscape(node.Destination, true)))
			case *ast.Image:
				links = append(links, string(util.URLEscape(node.Destination, true)))
				return ast.WalkSkipChildren, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return links
}

func resolveMarkdownLink(source, destination string) (string, string, bool, error) {
	if linkScheme.MatchString(destination) || strings.HasPrefix(destination, "//") {
		return "", "", false, nil
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return "", "", true, err
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return "", "", false, nil
	}
	target := source
	if strings.HasPrefix(parsed.Path, "/") {
		target = path.Clean(strings.TrimPrefix(parsed.Path, "/"))
	} else if parsed.Path != "" {
		target = path.Clean(path.Join(path.Dir(source), parsed.Path))
	}
	return target, strings.ToLower(parsed.Fragment), true, nil
}

func markdownAnchors(content string) map[string]bool { return parseMarkdown(content).anchors() }

func (document *markdownDocument) anchors() map[string]bool {
	if document.anchorSet != nil {
		return document.anchorSet
	}
	anchors := make(map[string]bool)
	_ = ast.Walk(document.root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && node.Kind() == ast.KindHeading {
			base := githubSlug(document.headingText(node))
			slug := base
			for suffix := 1; anchors[slug]; suffix++ {
				slug = fmt.Sprintf("%s-%d", base, suffix)
			}
			anchors[slug] = true
		}
		return ast.WalkContinue, nil
	})
	document.anchorSet = anchors
	return anchors
}

func (document *markdownDocument) headingText(node ast.Node) string {
	var rendered bytes.Buffer
	if err := document.markdown.Renderer().Render(&rendered, document.source, node); err != nil {
		return ""
	}
	root, err := xhtml.Parse(&rendered)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(htmlText(root))
}

func githubSlug(heading string) string {
	var slug strings.Builder
	for _, character := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(character), unicode.IsDigit(character), character == '-', character == '_':
			slug.WriteRune(character)
		case unicode.IsSpace(character):
			slug.WriteByte('-')
		}
	}
	return slug.String()
}
