package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const nontrivialChangeLines = 20
const reviewCheckpointWarning = 180

var publicGoDeclaration = regexp.MustCompile(
	`^\+\s*(func\s+(\([^)]*\)\s*)?[A-Z]|(const|type|var)\s+[A-Z])`,
)

type changedFile struct {
	path         string
	added        int
	lines        int
	renameSource bool
}

type changeSummary struct {
	changedLines      int
	addedProductionGo int
	productionGo      bool
	tests             bool
	docs              bool
	policy            bool
	topLevels         map[string]bool
}

func checkCommitChanges(repo, commit, message string) ([]finding, error) {
	files, err := readChangedFiles(repo, commit)
	if err != nil {
		return nil, err
	}
	summary := summarizeChanges(files)
	publicAPI, err := changesPublicGo(repo, commit)
	if err != nil {
		return nil, err
	}
	return changeFindings(summary, publicAPI, commit, message), nil
}

func summarizeChanges(files []changedFile) changeSummary {
	summary := changeSummary{topLevels: make(map[string]bool)}
	for _, file := range files {
		summary.changedLines += file.lines
		if isProductionGo(file.path) {
			summary.addedProductionGo += file.added
		}
		summary.topLevels[topLevel(file.path)] = true
		summary.tests = summary.tests || (!file.renameSource && isTestFile(file.path))
		summary.docs = summary.docs || strings.HasSuffix(file.path, ".md")
		summary.productionGo = summary.productionGo || isProductionGo(file.path)
		summary.policy = summary.policy || isReviewPolicy(file.path)
	}
	return summary
}

func changeFindings(summary changeSummary, publicAPI bool, commit, message string) []finding {
	var findings []finding
	if summary.changedLines >= nontrivialChangeLines && !hasCommitBody(message) {
		findings = append(findings, changeWarning(
			"commit-body", commit,
			"nontrivial change has no explanatory commit body",
		))
	}
	if summary.addedProductionGo >= reviewCheckpointWarning {
		findings = append(findings, changeWarning(
			"change-size", commit,
			fmt.Sprintf("commit adds %d non-test Go lines and is near the 200-line review checkpoint", summary.addedProductionGo),
		))
	}
	if summary.productionGo && !summary.tests {
		findings = append(findings, changeWarning(
			"tests-missing", commit,
			"production Go changed without a corresponding test change",
		))
	}
	if publicAPI && !summary.docs {
		findings = append(findings, changeWarning(
			"docs-missing", commit,
			"exported Go declarations changed without a documentation change",
		))
	}
	if len(summary.topLevels) >= 3 {
		findings = append(findings, changeWarning(
			"mixed-capabilities", commit,
			"commit spans three or more top-level areas; confirm it is one capability",
		))
	}
	if summary.policy {
		findings = append(findings, changeWarning(
			"review-policy", commit,
			"review-policy files changed and need explicit maintainer scrutiny",
		))
	}
	return findings
}

func readChangedFiles(repo, commit string) ([]changedFile, error) {
	output, err := gitOutput(repo, "diff", "--numstat", "-z", commit+"^", commit, "--")
	if err != nil {
		return nil, err
	}
	var files []changedFile
	records := bytes.Split(output, []byte{0})
	for index := 0; index < len(records)-1; index++ {
		fields := strings.SplitN(string(records[index]), "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("parse diff statistics for %s", commit)
		}
		added, addedErr := strconv.Atoi(fields[0])
		deleted, deletedErr := strconv.Atoi(fields[1])
		lines := 0
		if addedErr == nil && deletedErr == nil {
			lines = added + deleted
		}
		path := fields[2]
		if path == "" {
			if index+2 >= len(records)-1 {
				return nil, fmt.Errorf("parse renamed paths for %s", commit)
			}
			files = append(files, changedFile{
				path:         string(records[index+1]),
				renameSource: true,
			})
			path = string(records[index+2])
			index += 2
		}
		files = append(files, changedFile{path: path, added: added, lines: lines})
	}
	return files, nil
}

func changesPublicGo(repo, commit string) (bool, error) {
	output, err := gitOutput(
		repo, "diff", "--unified=0", commit+"^", commit, "--",
		":(glob)**/*.go", ":(glob,exclude)**/*_test.go",
	)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if publicGoDeclaration.MatchString(line) {
			return true, nil
		}
	}
	return false, nil
}

func hasCommitBody(message string) bool {
	parts := strings.SplitN(strings.TrimSuffix(message, "\n"), "\n\n", 2)
	return len(parts) == 2 && strings.TrimSpace(parts[1]) != ""
}

func isProductionGo(path string) bool {
	return strings.HasSuffix(path, ".go") && !isTestFile(path)
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/testdata/")
}

func isReviewPolicy(path string) bool {
	return path == "CONTRIBUTING.md" ||
		path == ".github/CODEOWNERS" ||
		path == ".github/pull_request_template.md" ||
		path == "AGENTS.md" ||
		strings.HasSuffix(path, "/AGENTS.md") ||
		strings.HasPrefix(path, ".github/workflows/") ||
		strings.HasPrefix(path, "internal/ci/")
}

func topLevel(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return parts[0]
}

func changeWarning(rule, commit, message string) finding {
	return newFinding(warningLevel, rule, commit, message)
}
