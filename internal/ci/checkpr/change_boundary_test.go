package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuralExportedDeclarationChanges(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		changed             bool
	}{
		{"grouped-const", "const (\n First=1\n)\n", "const (\n First=1\n Second=2\n)\n", true},
		{"grouped-var", "var (\n First int\n)\n", "var (\n First int\n Second int\n)\n", true},
		{"grouped-type", "type (\n First int\n)\n", "type (\n First int\n Second int\n)\n", true},
		{"removed", "type Removed int\n", "", true},
		{"field-type", "type Item struct {\n Value int\n}\n", "type Item struct {\n Value string\n}\n", true},
		{"function-body", "func Read() int { return 1 }\n", "func Read() int { return 2 }\n", false},
		{"private", "type (\n private int\n)\n", "type (\n private string\n)\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepository(t)
			commitPaths(t, repo, "Establish the declarations.\n", map[string]string{"device/api.go": "package device\n" + tc.before})
			head := commitPaths(t, repo, "Change the declarations.\n", map[string]string{"device/api.go": "package device\n" + tc.after})
			got, err := changesPublicGo(repo, head)
			if err != nil || got != tc.changed {
				t.Fatalf("public declaration change=%v, %v; want %v", got, err, tc.changed)
			}
		})
	}
}

func TestDeletedTestsAndDocsDoNotSupportProductionChanges(t *testing.T) {
	repo := newRepository(t)
	commitPaths(t, repo, "Establish source and supporting files.\n", map[string]string{"device.go": "package device\n", "device_test.go": "package device\n", "guide.md": "# Guide\n"})
	for _, name := range []string{"device_test.go", "guide.md"} {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	head := commitPaths(t, repo, "Change the public operation.\n", map[string]string{"device.go": "package device\nfunc Open() {}\n"})
	files, err := readChangedFiles(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeChanges(files)
	if summary.tests || summary.docs {
		t.Fatalf("deleted files counted as supporting additions: %+v", summary)
	}
}

func TestHeadAttributesCannotHideGoChanges(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	code := "package device\nfunc Open() {}\n" + strings.Repeat("// code\n", 210)
	head := commitPaths(t, repo, "Add the public device.\n", map[string]string{".gitattributes": "*.go -diff\n", "device.go": code})
	files, err := readChangedFiles(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeChanges(files)
	if summary.addedProductionGo != strings.Count(code, "\n") || !summary.policy {
		t.Errorf("attributes hid Go additions or policy input: %+v", summary)
	}
	api, err := changesPublicGo(repo, head)
	if err != nil || !api {
		t.Fatalf("attributes hid public API: %v, %v", api, err)
	}
}

func TestPromotionIntoProductionCountsWholeDestination(t *testing.T) {
	for _, source := range []string{"helper_test.go", "helper.txt", "testdata/helper.go"} {
		t.Run(source, func(t *testing.T) {
			repo := newRepository(t)
			code := "package device\nfunc Open() {}\n" + strings.Repeat("// implementation\n", 190)
			commitPaths(t, repo, "Establish donor contents.\n", map[string]string{source: code})
			if err := os.Rename(filepath.Join(repo, source), filepath.Join(repo, "device.go")); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "--all")
			runGitInput(t, repo, "Promote the implementation.\n", "commit", "-q", "-F", "-")
			head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			files, err := readChangedFiles(repo, head)
			if err != nil {
				t.Fatal(err)
			}
			if got := summarizeChanges(files).addedProductionGo; got != strings.Count(code, "\n") {
				t.Fatalf("promotion count=%d, want %d", got, strings.Count(code, "\n"))
			}
		})
	}
}

func TestChangeSizeWarningDistinguishesCheckpointRanges(t *testing.T) {
	for _, tc := range []struct {
		lines int
		want  string
	}{{180, "near"}, {200, "near"}, {201, "exceeds"}, {301, "approved"}} {
		findings := changeFindings(changeSummary{addedProductionGo: tc.lines}, false, "head", "Message.\n\nReason.\n")
		found := false
		for _, f := range findings {
			if f.rule == "change-size" {
				found = true
				if !strings.Contains(f.message, tc.want) {
					t.Errorf("size %d: %s", tc.lines, f.message)
				}
			}
		}
		if !found {
			t.Errorf("no size warning at %d", tc.lines)
		}
	}
}

func TestReviewPolicyWarningsPrecedeOrdinaryAnnotations(t *testing.T) {
	repo := newRepository(t)
	base := commit(t, repo, "Establish the base.\n")
	for i := range 6 {
		commitPaths(t, repo, fmt.Sprintf("Add operation %d.\n", i), map[string]string{fmt.Sprintf("device%d.go", i): fmt.Sprintf("package device\nfunc Operation%d() {}\n", i)})
	}
	head := commitPaths(t, repo, "Adjust review guidance.\n", map[string]string{"nested/AGENTS.md": "Review carefully.\n"})
	findings, err := checkRange(repo, base, head)
	if err != nil {
		t.Fatal(err)
	}
	warnings := []finding{}
	for _, f := range findings {
		if f.level == warningLevel {
			warnings = append(warnings, f)
		}
	}
	if len(warnings) <= 10 || warnings[0].rule != "review-policy" {
		t.Fatalf("policy warning lost behind annotation limit: %+v", warnings)
	}
}

func TestPolicyDoesNotExecuteExternalDiffDriver(t *testing.T) {
	repo := newRepository(t)
	commitPaths(t, repo, "Establish the source.\n", map[string]string{"device.go": "package device\nfunc Open() {}\n"})
	marker := filepath.Join(repo, "driver-ran")
	t.Setenv("OSTIOLE_DIFF_MARKER", marker)
	driver := filepath.Join(repo, "external-diff")
	if err := os.WriteFile(driver, []byte("#!/bin/sh\nprintf invoked > \"$OSTIOLE_DIFF_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "diff.external", "'"+strings.ReplaceAll(driver, "'", "'\\''")+"'")
	head := commitPaths(t, repo, "Change the signature.\n", map[string]string{"device.go": "package device\nfunc Open(value int) {}\n"})
	files, err := readChangedFiles(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if summarizeChanges(files).addedProductionGo != 1 {
		t.Fatal("external driver changed statistics")
	}
	changed, err := changesPublicGo(repo, head)
	if err != nil || !changed {
		t.Fatalf("signature comparison: %v, %v", changed, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("executed external diff driver: %v", err)
	}
}

func TestExportedSetsIncludeReceiverAndFieldSignatures(t *testing.T) {
	repo := newRepository(t)
	before := "package device\ntype First struct { Value int }\ntype Second struct{}\nfunc (*First) Read() int { return 1 }\nfunc (*Second) Read() int { return 2 }\n"
	base := commitPaths(t, repo, "Establish public signatures.\n", map[string]string{"api.go": before})
	head := commitPaths(t, repo, "Change one receiver signature.\n", map[string]string{"api.go": strings.Replace(before, "(*First) Read() int { return 1 }", "(*First) Read() string { return `value` }", 1)})
	files, err := readChangedFiles(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	oldSet, err := exportedGoAt(repo, base, files)
	if err != nil {
		t.Fatal(err)
	}
	newSet, err := exportedGoAt(repo, head, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldSet) != 4 || len(newSet) != 4 {
		t.Fatalf("receiver declarations collapsed: %v / %v", oldSet, newSet)
	}
	removed, added := 0, 0
	for key := range oldSet {
		if !newSet[key] {
			removed++
			if !strings.Contains(key, "(*First) Read() int") {
				t.Errorf("unexpected removal: %s", key)
			}
		}
	}
	for key := range newSet {
		if !oldSet[key] {
			added++
			if !strings.Contains(key, "(*First) Read() string") {
				t.Errorf("unexpected addition: %s", key)
			}
		}
	}
	if removed != 1 || added != 1 {
		t.Fatalf("changed declaration set: removed%d added%d", removed, added)
	}
}

func TestCommittedGoReadsUseLiteralPaths(t *testing.T) {
	repo := newRepository(t)
	exact := "package device\nfunc Exact() {}\n"
	head := commitPaths(t, repo, "Establish literal paths.\n", map[string]string{"api[1].go": exact, "api1.go": "package device\nfunc Other() {}\n"})
	data, exists, err := readGitFile(repo, head, "api[1].go")
	if err != nil || !exists || string(data) != exact {
		t.Fatalf("read different committed path: %q, %v, %v", data, exists, err)
	}
}

func TestTestSourcesDoNotChangePublicDeclarations(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	head := commitPaths(t, repo, "Add tests and fixtures.\n", map[string]string{"device_test.go": "package device\nfunc TestPublicName() {}\n", "testdata/fixture.go": "package device\nfunc PublicFixture() {}\n"})
	changed, err := changesPublicGo(repo, head)
	if err != nil || changed {
		t.Fatalf("test source classified as public API: %v, %v", changed, err)
	}
}

func TestMalformedCommittedGoFailsDeclarationInspection(t *testing.T) {
	repo := newRepository(t)
	commit(t, repo, "Establish the base.\n")
	head := commitPaths(t, repo, "Introduce malformed source.\n", map[string]string{"device.go": "this is not Go source\n"})
	if _, err := changesPublicGo(repo, head); err == nil || !strings.Contains(err.Error(), "device.go") {
		t.Fatalf("malformed source silently inspected: %v", err)
	}
}

func TestCommittedGoReadsDoNotRunHeadTextconv(t *testing.T) {
	repo := newRepository(t)
	commitPaths(t, repo, "Establish the source.\n", map[string]string{"device.go": "package device\nfunc Open() {}\n"})
	marker := filepath.Join(repo, "textconv-ran")
	t.Setenv("OSTIOLE_TEXTCONV_MARKER", marker)
	driver := filepath.Join(repo, "textconv")
	if err := os.WriteFile(driver, []byte("#!/bin/sh\nprintf invoked > \"$OSTIOLE_TEXTCONV_MARKER\"\ncat \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := "'" + strings.ReplaceAll(driver, "'", "'\\''") + "'"
	runGit(t, repo, "config", "diff.custom.textconv", command)
	head := commitPaths(t, repo, "Change the public signature.\n", map[string]string{".gitattributes": "*.go diff=custom\n", "device.go": "package device\nfunc Open(value int) {}\n"})
	changed, err := changesPublicGo(repo, head)
	if err != nil || !changed {
		t.Fatalf("committed Go comparison: %v, %v", changed, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("executed head-selected text conversion: %v", err)
	}
}
