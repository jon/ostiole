package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

var inlineLink = regexp.MustCompile(`!?\[[^]]*\]\(([^)]+)\)`)
var referenceLink = regexp.MustCompile(`^\s*\[[^]]+\]:\s*(\S+)`)

type markdownTree struct {
	repo          string
	head          string
	files         map[string]bool
	directories   map[string]bool
	markdownFiles []string
	contents      map[string]string
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
		directories: make(map[string]bool),
		contents:    make(map[string]string),
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

func (tree *markdownTree) readFile(name string) (string, error) {
	if content, ok := tree.contents[name]; ok {
		return content, nil
	}
	content, err := gitOutput(tree.repo, "show", tree.head+":"+name)
	if err != nil {
		return "", err
	}
	tree.contents[name] = string(content)
	return string(content), nil
}

func (tree *markdownTree) checkFile(source string) ([]finding, error) {
	content, err := tree.readFile(source)
	if err != nil {
		return nil, err
	}
	var findings []finding
	for _, link := range markdownLinks(content) {
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
	target, fragment, local := resolveMarkdownLink(source, link)
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
	targetContent, err := tree.readFile(target)
	if err != nil {
		return nil, err
	}
	if markdownAnchors(targetContent)[fragment] {
		return nil, nil
	}
	result := newFinding(
		errorLevel, "markdown-anchor", tree.head,
		fmt.Sprintf("%s links to missing anchor %q in %s", source, fragment, target),
	)
	return &result, nil
}

func markdownLinks(content string) []string {
	var links []string
	inFence := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		line = removeInlineCode(line)
		for _, match := range inlineLink.FindAllStringSubmatch(line, -1) {
			links = append(links, linkDestination(match[1]))
		}
		if match := referenceLink.FindStringSubmatch(line); match != nil {
			links = append(links, linkDestination(match[1]))
		}
	}
	return links
}

func linkDestination(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<") {
		if end := strings.Index(value, ">"); end >= 0 {
			return value[1:end]
		}
	}
	if fields := strings.Fields(value); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func resolveMarkdownLink(source, destination string) (string, string, bool) {
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(destination, "/") {
		return "", "", false
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", "", false
	}
	target := source
	if decodedPath != "" {
		target = path.Clean(path.Join(path.Dir(source), decodedPath))
	}
	if target == ".." || strings.HasPrefix(target, "../") {
		return target, "", true
	}
	fragment, err := url.PathUnescape(parsed.Fragment)
	if err != nil {
		fragment = parsed.Fragment
	}
	return target, strings.ToLower(fragment), true
}

func markdownAnchors(content string) map[string]bool {
	anchors := make(map[string]bool)
	counts := make(map[string]int)
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimLeft(trimmed, "#")
		if heading == trimmed || !strings.HasPrefix(heading, " ") {
			continue
		}
		heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
		base := githubSlug(heading)
		slug := base
		if counts[base] > 0 {
			slug = fmt.Sprintf("%s-%d", base, counts[base])
		}
		counts[base]++
		anchors[slug] = true
	}
	return anchors
}

func githubSlug(heading string) string {
	var slug strings.Builder
	for _, character := range strings.ToLower(removeInlineCode(heading)) {
		switch {
		case unicode.IsLetter(character), unicode.IsDigit(character), character == '-', character == '_':
			slug.WriteRune(character)
		case unicode.IsSpace(character):
			slug.WriteByte('-')
		}
	}
	return slug.String()
}

func removeInlineCode(line string) string {
	var output strings.Builder
	inCode := false
	for _, character := range line {
		if character == '`' {
			inCode = !inCode
			continue
		}
		if !inCode {
			output.WriteRune(character)
		}
	}
	return output.String()
}
