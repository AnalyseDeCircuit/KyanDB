package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/AnalyseDeCircuit/kyandb"

// TestArchitectureBoundaries enforces the package dependency DAG and zero-CGO
// invariants defined in docs/ARCHITECTURE.md on every `go test ./...` run.
func TestArchitectureBoundaries(t *testing.T) {
	fset := token.NewFileSet()

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "build" || name == ".mygo" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}

		relDir := filepath.ToSlash(filepath.Dir(path))
		isTest := strings.HasSuffix(path, "_test.go")

		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Errorf("%s: unquote import %s: %v", path, imp.Path.Value, err)
				continue
			}
			checkImportRule(t, path, relDir, importPath, isTest)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

func checkImportRule(t *testing.T, filePath, relDir, imp string, isTest bool) {
	t.Helper()

	// Global rule: Pure Go, zero CGO across the entire repository.
	if imp == "C" {
		t.Errorf("%s: CGO import %q is forbidden (see docs/ARCHITECTURE.md)", filePath, imp)
	}
	if imp == "github.com/mattn/go-sqlite3" {
		t.Errorf("%s: CGO driver %q is forbidden; use pure-Go modernc.org/sqlite", filePath, imp)
	}

	if !strings.HasPrefix(relDir, "internal") {
		return
	}

	subpkg := strings.TrimPrefix(relDir, "internal/")
	topLayer := subpkg
	if idx := strings.IndexByte(subpkg, '/'); idx >= 0 {
		topLayer = subpkg[:idx]
	}

	internalPrefix := modulePath + "/internal/"

	// Non-UI internal packages must stay headless and never import mygo/ui.
	if topLayer != "ui" && (imp == "github.com/egoist/mygo/ui" || strings.HasPrefix(imp, "github.com/egoist/mygo/ui/")) {
		t.Errorf("%s: non-UI package %q must not import %q (see docs/ARCHITECTURE.md)", filePath, relDir, imp)
	}

	// User-facing copy is chosen in the window's language by the UI. Core
	// layers return typed domain errors and values, never translated text.
	if topLayer != "ui" && (imp == modulePath+"/locales" || strings.HasPrefix(imp, modulePath+"/locales/")) {
		t.Errorf("%s: non-UI package %q must not import %q; return domain errors and let internal/ui translate", filePath, relDir, imp)
	}

	switch topLayer {
	case "domain":
		// internal/domain must have zero third-party or internal dependencies (stdlib only).
		if strings.Contains(imp, ".") {
			t.Errorf("%s: internal/domain must only import Go standard library, got %q", filePath, imp)
		}

	case "ui":
		// UI must talk to runtime/domain only, never directly to DB drivers, tunnels, config, or secrets.
		forbidden := []string{
			"database/sql",
			internalPrefix + "engine",
			internalPrefix + "tunnel",
			internalPrefix + "config",
			internalPrefix + "secret",
		}
		for _, prefix := range forbidden {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				t.Errorf("%s: UI package %q must not import %q directly; route through internal/runtime", filePath, relDir, imp)
			}
		}

	case "config":
		// Plaintext config layer must never handle live secrets, engines, tunnels, or runtime pools.
		forbidden := []string{
			internalPrefix + "secret",
			internalPrefix + "engine",
			internalPrefix + "tunnel",
			internalPrefix + "runtime",
		}
		for _, prefix := range forbidden {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				t.Errorf("%s: internal/config must not import %q (see docs/ARCHITECTURE.md)", filePath, imp)
			}
		}

	case "sqlguard":
		// SQL classification & diff generation must stay pure and side-effect free.
		forbidden := []string{
			"database/sql",
			internalPrefix + "engine",
			internalPrefix + "tunnel",
			internalPrefix + "secret",
			internalPrefix + "runtime",
		}
		for _, prefix := range forbidden {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				t.Errorf("%s: internal/sqlguard must not import %q", filePath, imp)
			}
		}

	case "mcp":
		// MCP must route all queries through internal/runtime so SafetyGate, leases,
		// and AuditLog cannot be bypassed, and raw secrets are never exposed.
		forbidden := []string{
			"database/sql",
			internalPrefix + "engine",
			internalPrefix + "tunnel",
			internalPrefix + "secret",
			internalPrefix + "config",
		}
		for _, prefix := range forbidden {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				t.Errorf("%s: internal/mcp must not import %q directly; route through internal/runtime", filePath, imp)
			}
		}

	case "secret":
		// Secret layer must not depend on higher-level config, engine, tunnel, runtime, or ui.
		if !isTest {
			forbidden := []string{
				internalPrefix + "config",
				internalPrefix + "engine",
				internalPrefix + "tunnel",
				internalPrefix + "runtime",
				internalPrefix + "ui",
			}
			for _, prefix := range forbidden {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					t.Errorf("%s: internal/secret must not import %q", filePath, imp)
				}
			}
		}
	}
}
