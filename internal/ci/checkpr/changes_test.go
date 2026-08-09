package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCommitChangesWarnsAboutReviewShape(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	code := "package device\n\nfunc Open() {}\n" + strings.Repeat("// changed\n", 178)
	head := commitPaths(t, repo, "Add the device implementation.\n", map[string]string{
		"device/device.go": code,
	})

	findings, err := checkCommitChanges(repo, head, "Add the device implementation.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	for _, rule := range []string{"commit-body", "change-size", "tests-missing", "docs-missing"} {
		if !hasFinding(findings, warningLevel, rule) {
			t.Errorf("findings = %#v, want warning %q", findings, rule)
		}
	}
}

func TestCheckCommitChangesWarnsAboutPolicyFiles(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	head := commitPaths(t, repo, "Adjust the review policy.\n", map[string]string{
		"usb/AGENTS.md": "Review carefully.\n",
	})

	findings, err := checkCommitChanges(repo, head, "Adjust the review policy.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	if !hasFinding(findings, warningLevel, "review-policy") {
		t.Fatalf("findings = %#v, want review-policy warning", findings)
	}
}

func TestCheckCommitChangesWarnsAboutRenamedPolicyFiles(t *testing.T) {
	repo := newRepository(t)
	commitPaths(t, repo, "Establish the base.\n", map[string]string{
		"review-guidance.md": "Review carefully.\n",
	})
	newPath := filepath.Join(repo, "usb", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "review-guidance.md"), newPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "--all")
	runGitInput(t, repo, "Move the review guidance.\n", "commit", "-q", "-F", "-")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	findings, err := checkCommitChanges(repo, head, "Move the review guidance.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	if !hasFinding(findings, warningLevel, "review-policy") {
		t.Fatalf("findings = %#v, want review-policy warning", findings)
	}
}

func TestCheckCommitChangesDoesNotCountPureMovementAsAddedGo(t *testing.T) {
	repo := newRepository(t)
	code := "package device\n\n" + strings.Repeat("// implementation\n", 180)
	commitPaths(t, repo, "Establish the base.\n", map[string]string{
		"device.go": code,
	})
	newPath := filepath.Join(repo, "device", "device.go")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "device.go"), newPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "--all")
	runGitInput(t, repo, "Move the device implementation.\n", "commit", "-q", "-F", "-")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	findings, err := checkCommitChanges(repo, head, "Move the device implementation.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	if hasFinding(findings, warningLevel, "change-size") {
		t.Fatalf("findings = %#v, do not want change-size warning", findings)
	}
	if !hasFinding(findings, warningLevel, "tests-missing") {
		t.Fatalf("findings = %#v, want tests-missing warning", findings)
	}
}

func TestCheckCommitChangesDoesNotCountRenamedTestSource(t *testing.T) {
	repo := newRepository(t)
	commitPaths(t, repo, "Establish the base.\n", map[string]string{
		"helper_test.go": "package device\n\nfunc helper() {}\n",
	})
	if err := os.Rename(
		filepath.Join(repo, "helper_test.go"),
		filepath.Join(repo, "helper.go"),
	); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "--all")
	runGitInput(t, repo, "Promote the test helper.\n", "commit", "-q", "-F", "-")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	findings, err := checkCommitChanges(repo, head, "Promote the test helper.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	if !hasFinding(findings, warningLevel, "tests-missing") {
		t.Fatalf("findings = %#v, want tests-missing warning", findings)
	}
}

func TestCheckCommitChangesWarnsAboutMixedCapabilities(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	head := commitPaths(t, repo, "Combine unrelated capabilities.\n\nExplain the combined change.\n", map[string]string{
		"device/device.go":  "package device\n",
		"probe/probe.go":    "package probe\n",
		"transport/wire.go": "package transport\n",
	})

	findings, err := checkCommitChanges(repo, head, "Combine unrelated capabilities.\n\nExplain the combined change.\n")
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	if !hasFinding(findings, warningLevel, "mixed-capabilities") {
		t.Fatalf("findings = %#v, want mixed-capabilities warning", findings)
	}
}

func TestCheckCommitChangesAcceptsSupportingTestsAndDocs(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	message := "Add a documented device operation.\n\nExplain the behavior and its validation.\n"
	head := commitPaths(t, repo, message, map[string]string{
		"device/device.go":      "package device\n\nfunc Open() {}\n",
		"device/device_test.go": "package device\n",
		"docs/device.md":        "# Device\n",
	})

	findings, err := checkCommitChanges(repo, head, message)
	if err != nil {
		t.Fatalf("checkCommitChanges() error = %v", err)
	}
	for _, rule := range []string{"commit-body", "tests-missing", "docs-missing"} {
		if hasFinding(findings, warningLevel, rule) {
			t.Errorf("findings = %#v, do not want warning %q", findings, rule)
		}
	}
}

func commitPaths(t *testing.T, repo, message string, files map[string]string) string {
	t.Helper()
	for name, contents := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "--all")
	runGitInput(t, repo, message, "commit", "-q", "-F", "-")
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}
