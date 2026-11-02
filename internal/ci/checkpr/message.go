package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type severity string

const (
	warningLevel severity = "warning"
	errorLevel   severity = "error"
)

type finding struct {
	level   severity
	rule    string
	commit  string
	message string
}

var conventionalSubject = regexp.MustCompile(
	`(?i)^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([^)]*\))?!?:`,
)

var trailerLine = regexp.MustCompile(`^[A-Za-z0-9-]+: \S`)

func checkMessage(commit, message string) []finding {
	lines := strings.Split(strings.TrimSuffix(message, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return []finding{newFinding(errorLevel, "subject-empty", commit, "subject is empty")}
	}

	subject := lines[0]
	findings := checkSubject(commit, subject)
	if len(lines) > 1 && lines[1] != "" {
		findings = append(findings, newFinding(
			errorLevel, "body-separator", commit,
			"commit body must follow a blank line",
		))
	}
	if len(lines) > 2 && lines[1] == "" {
		findings = append(findings, checkBody(commit, lines[2:])...)
	}
	return findings
}

func checkSubject(commit, subject string) []finding {
	var findings []finding
	first, _ := utf8.DecodeRuneInString(subject)
	if !unicode.IsUpper(first) {
		findings = append(findings, newFinding(
			errorLevel, "subject-case", commit,
			"subject must begin with a capital letter",
		))
	}
	if !strings.HasSuffix(subject, ".") {
		findings = append(findings, newFinding(
			errorLevel, "subject-period", commit,
			"subject must end with a period",
		))
	}
	if conventionalSubject.MatchString(subject) {
		findings = append(findings, newFinding(
			errorLevel, "subject-prefix", commit,
			"subject must not use a category or scope prefix",
		))
	}
	findings = append(findings, checkSubjectLength(commit, subject)...)
	if hasNonImperativePrefix(subject) {
		findings = append(findings, newFinding(
			warningLevel, "subject-mood", commit,
			"subject appears non-imperative; describe the resulting change",
		))
	}
	return findings
}

func checkSubjectLength(commit, subject string) []finding {
	length := utf8.RuneCountInString(subject)
	if length > 120 {
		return []finding{newFinding(
			errorLevel, "subject-length", commit,
			fmt.Sprintf("subject is %d columns; maximum is 120", length),
		)}
	}
	if length > 72 {
		return []finding{newFinding(
			warningLevel, "subject-length", commit,
			fmt.Sprintf("subject is %d columns; prefer 72 or fewer", length),
		)}
	}
	return nil
}

func checkBody(commit string, lines []string) []finding {
	var findings []finding
	trailers := trailerStart(lines)
	code := fencedBodyLines(lines)
	for index, line := range lines {
		if index >= trailers || code[index] || utf8.RuneCountInString(line) <= 72 || !wrappableProse(line) {
			continue
		}
		findings = append(findings, newFinding(
			errorLevel, "body-length", commit,
			fmt.Sprintf("body prose exceeds 72 columns: %q", line),
		))
	}
	return findings
}

func trailerStart(lines []string) int {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		start--
	}
	if start == end || !trailerLine.MatchString(lines[start]) {
		return len(lines)
	}
	for _, line := range lines[start:end] {
		if !trailerLine.MatchString(line) && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			return len(lines)
		}
	}
	return start
}

func fencedBodyLines(lines []string) []bool {
	code := make([]bool, len(lines))
	for start := 0; start < len(lines); start++ {
		marker, size := bodyFence(lines[start])
		if size < 3 {
			continue
		}
		closed := false
		for end := start + 1; end < len(lines); end++ {
			closeMarker, closeSize := bodyFence(lines[end])
			trimmed := strings.TrimSpace(lines[end])
			if closeMarker != marker || closeSize < size || strings.Trim(trimmed, string(marker)) != "" {
				continue
			}
			for index := start; index <= end; index++ {
				code[index] = true
			}
			start = end
			closed = true
			break
		}
		if !closed {
			break
		}
	}
	return code
}

func bodyFence(line string) (byte, int) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) == 0 || trimmed[0] != '\x60' && trimmed[0] != '~' {
		return 0, 0
	}
	marker := trimmed[0]
	size := 0
	for size < len(trimmed) && trimmed[size] == marker {
		size++
	}
	if marker == '\x60' && strings.ContainsRune(trimmed[size:], '\x60') {
		return 0, 0
	}
	return marker, size
}

func wrappableProse(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ") {
		return false
	}
	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "$ ") {
		return false
	}
	if wholeBodyCodeSpan(trimmed) {
		return false
	}
	return strings.Contains(trimmed, " ")
}

func wholeBodyCodeSpan(line string) bool {
	size := len(line) - len(strings.TrimLeft(line, "`"))
	if size == 0 || len(line) <= 2*size || len(line)-len(strings.TrimRight(line, "`")) != size {
		return false
	}
	for _, run := range strings.FieldsFunc(line[size:len(line)-size], func(r rune) bool { return r != '`' }) {
		if len(run) == size {
			return false
		}
	}
	return true
}

func hasNonImperativePrefix(subject string) bool {
	lower := strings.ToLower(subject)
	for _, prefix := range []string{
		"added ", "adding ", "fixed ", "fixing ", "this ",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func newFinding(level severity, rule, commit, message string) finding {
	return finding{level: level, rule: rule, commit: commit, message: message}
}

func hasErrors(findings []finding) bool {
	for _, finding := range findings {
		if finding.level == errorLevel {
			return true
		}
	}
	return false
}
