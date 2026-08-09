// Command checkpr validates the commits introduced by a pull request.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("checkpr", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository working tree")
	base := flags.String("base", "", "pull-request base commit")
	head := flags.String("head", "", "pull-request head commit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *base == "" || *head == "" {
		fmt.Fprintln(stderr, "checkpr: -base and -head are required")
		return 2
	}

	findings, err := checkRange(*repo, *base, *head)
	if err != nil {
		fmt.Fprintf(stderr, "checkpr: %v\n", err)
		return 2
	}
	for _, finding := range findings {
		writeFinding(stdout, finding)
	}
	if hasErrors(findings) {
		return 1
	}
	return 0
}

func checkRange(repo, base, head string) ([]finding, error) {
	output, err := gitOutput(repo, "rev-list", "--reverse", base+".."+head)
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(string(output))
	if len(commits) == 0 {
		return nil, errors.New("pull-request commit range is empty")
	}

	var findings []finding
	for _, commit := range commits {
		metadata, err := gitOutput(repo, "show", "-s", "--format=%P%x00%B", commit)
		if err != nil {
			return nil, err
		}
		parts := bytes.SplitN(metadata, []byte{0}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("read commit metadata for %s", commit)
		}
		if len(strings.Fields(string(parts[0]))) != 1 {
			findings = append(findings, newFinding(
				errorLevel, "linear-history", commit,
				"commit must have exactly one parent",
			))
		}
		findings = append(findings, checkMessage(commit, string(parts[1]))...)
	}
	return findings, nil
}

func writeFinding(output io.Writer, finding finding) {
	commit := finding.commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		title := escapeAnnotation(finding.rule + " in " + commit)
		message := escapeAnnotation(finding.message)
		fmt.Fprintf(output, "::%s title=%s::%s\n", finding.level, title, message)
		return
	}
	format := "%s[%s] %s: %s\n"
	fmt.Fprintf(output, format, finding.level, finding.rule, commit, finding.message)
}

func escapeAnnotation(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "\r", "%0D")
	value = strings.ReplaceAll(value, "\n", "%0A")
	value = strings.ReplaceAll(value, ":", "%3A")
	value = strings.ReplaceAll(value, ",", "%2C")
	return value
}

func gitOutput(repo string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(output))
	}
	return output, nil
}
