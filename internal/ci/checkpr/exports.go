package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"maps"
	"strings"
)

func changesPublicGo(repo, commit string) (bool, error) {
	files, err := readChangedFiles(repo, commit)
	if err != nil {
		return false, err
	}
	before, err := exportedGoAt(repo, commit+"^", files)
	if err != nil {
		return false, err
	}
	after, err := exportedGoAt(repo, commit, files)
	return !maps.Equal(before, after), err
}

func exportedGoAt(repo, revision string, files []changedFile) (map[string]bool, error) {
	exports := make(map[string]bool)
	for _, file := range files {
		if !isProductionGo(file.path) {
			continue
		}
		data, exists, err := readGitFile(repo, revision, file.path)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, file.path, data, 0)
		if err != nil {
			return nil, fmt.Errorf("parse public declarations in %s at %s: %w", file.path, revision, err)
		}
		for _, declaration := range parsed.Decls {
			if err := collectExports(exports, file.path, set, declaration); err != nil {
				return nil, err
			}
		}
	}
	return exports, nil
}

func collectExports(exports map[string]bool, path string, set *token.FileSet, declaration ast.Decl) error {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		if d.Name.IsExported() {
			signature := *d
			signature.Body = nil
			return addGoExport(exports, path, "func", set, &signature)
		}
	case *ast.GenDecl:
		for _, specification := range d.Specs {
			node := exportedSpecification(specification)
			if node == nil {
				continue
			}
			if d.Tok == token.CONST {
				node = d
			}
			if err := addGoExport(exports, path, d.Tok.String(), set, node); err != nil {
				return err
			}
		}
	}
	return nil
}

func addGoExport(exports map[string]bool, path, kind string, set *token.FileSet, node ast.Node) error {
	var output bytes.Buffer
	if err := format.Node(&output, set, node); err != nil {
		return fmt.Errorf("format exported declaration in %s: %w", path, err)
	}
	exports[path+"/"+kind+"/"+output.String()] = true
	return nil
}

func readGitFile(repo, revision, path string) ([]byte, bool, error) {
	entry, err := gitOutput(repo, "--literal-pathspecs", "ls-tree", "-z", revision, "--", path)
	if err != nil || len(entry) == 0 {
		return nil, false, err
	}
	metadata := strings.Fields(strings.SplitN(string(entry), "\t", 2)[0])
	if len(metadata) != 3 || metadata[1] != "blob" {
		return nil, false, fmt.Errorf("read Git blob for %s at %s", path, revision)
	}
	data, err := gitOutput(repo, "cat-file", "blob", metadata[2])
	return data, true, err
}

func exportedSpecification(specification ast.Spec) ast.Node {
	switch spec := specification.(type) {
	case *ast.TypeSpec:
		if spec.Name.IsExported() {
			return spec
		}
	case *ast.ValueSpec:
		for _, name := range spec.Names {
			if name.IsExported() {
				return spec
			}
		}
	}
	return nil
}
