package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsInvalidFixture(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	repo := newRepository(t)
	base := commit(t, repo, "Establish the base.\n")
	head := commit(t, repo, "break the policy\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	status := run([]string{
		"-repo", repo,
		"-base", base,
		"-head", head,
	}, &stdout, &stderr)

	if status != 1 {
		t.Fatalf("run() = %d, want policy failure; stderr = %q", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "error[subject-case]") {
		t.Fatalf("stdout = %q, want subject-case finding", stdout.String())
	}
}

func TestWriteFindingUsesGitHubAnnotations(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	finding := newFinding(
		errorLevel,
		"subject-case",
		"abc123",
		"subject must begin with a capital letter",
	)
	var output bytes.Buffer

	writeFinding(&output, finding)

	want := "::error title=subject-case in abc123::" +
		"subject must begin with a capital letter\n"
	if output.String() != want {
		t.Fatalf("writeFinding() = %q, want %q", output.String(), want)
	}
}

func TestCheckRangeRejectsMergeCommits(t *testing.T) {
	repo := newRepository(t)
	base := commit(t, repo, "Establish the base.\n")
	first := commit(t, repo, "Add the first branch.\n")
	runGit(t, repo, "checkout", "-q", "-b", "side", base)
	side := commit(t, repo, "Add the side branch.\n")
	runGit(t, repo, "checkout", "-q", "main")
	tree := strings.TrimSpace(runGit(t, repo, "write-tree"))
	merge := strings.TrimSpace(runGitInput(
		t,
		repo,
		"Merge the branches.\n",
		"commit-tree", tree, "-p", first, "-p", side,
	))

	findings, err := checkRange(repo, base, merge)
	if err != nil {
		t.Fatalf("checkRange() error = %v", err)
	}
	if !hasFinding(findings, errorLevel, "linear-history") {
		t.Fatalf("checkRange() = %#v, want linear-history error", findings)
	}
}

func TestCheckRangeRejectsEmptyRanges(t *testing.T) {
	repo := newRepository(t)
	base := commit(t, repo, "Establish the base.\n")
	if _, err := checkRange(repo, base, base); err == nil {
		t.Fatal("checkRange() error = nil, want empty range error")
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.name", "Policy Test")
	runGit(t, repo, "config", "user.email", "policy@example.com")
	return repo
}

func commit(t *testing.T, repo, message string) string {
	t.Helper()
	path := filepath.Join(repo, "change")
	contents, _ := os.ReadFile(path)
	contents = append(contents, 'x')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "change")
	runGitInput(t, repo, message, "commit", "-q", "-F", "-")
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	return runGitInput(t, repo, "", args...)
}

func runGitInput(t *testing.T, repo, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repo
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
