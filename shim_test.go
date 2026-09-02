package claudecli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeShimLayout builds a fake npm install: a shim file plus the CLI
// package at pkgRel (relative to the shim's directory), with the given
// package.json content and entry script.
func writeShimLayout(t *testing.T, shimName, pkgRel, pkgJSON, entryName string) (shimPath, entryPath string) {
	t.Helper()
	root := t.TempDir()
	shimPath = filepath.Join(root, shimName)
	if err := os.WriteFile(shimPath, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, pkgRel)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if pkgJSON != "" {
		if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if entryName != "" {
		entryPath = filepath.Join(pkgDir, entryName)
		if err := os.WriteFile(entryPath, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return shimPath, entryPath
}

const cliPkgJSON = `{"name":"@anthropic-ai/claude-code","bin":{"claude":"cli.js"}}`

func TestFindShimCLIJS_GlobalNPMLayout(t *testing.T) {
	shim, entry := writeShimLayout(t, "claude.cmd",
		filepath.Join("node_modules", "@anthropic-ai", "claude-code"), cliPkgJSON, "cli.js")
	got, ok := findShimCLIJS(shim)
	if !ok || got != entry {
		t.Fatalf("findShimCLIJS = %q, %v; want %q, true", got, ok, entry)
	}
}

func TestFindShimCLIJS_UnixPrefixLayout(t *testing.T) {
	// prefix/bin/claude.cmd with the package under prefix/lib/node_modules.
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(binDir, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(cliPkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(pkgDir, "cli.js")
	if err := os.WriteFile(entry, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := findShimCLIJS(shim)
	if !ok || got != entry {
		t.Fatalf("findShimCLIJS = %q, %v; want %q, true", got, ok, entry)
	}
}

func TestFindShimCLIJS_BinAsString(t *testing.T) {
	shim, entry := writeShimLayout(t, "claude.cmd",
		filepath.Join("node_modules", "@anthropic-ai", "claude-code"),
		`{"name":"@anthropic-ai/claude-code","bin":"main.js"}`, "main.js")
	got, ok := findShimCLIJS(shim)
	if !ok || got != entry {
		t.Fatalf("findShimCLIJS = %q, %v; want %q, true", got, ok, entry)
	}
}

func TestFindShimCLIJS_NoBypass(t *testing.T) {
	pkgRel := filepath.Join("node_modules", "@anthropic-ai", "claude-code")
	cases := []struct {
		name              string
		shimName, pkgJSON string
		entry             string
	}{
		{"foreign package", "claude.cmd", `{"name":"someone-else/claude"}`, "cli.js"},
		{"missing package.json", "claude.cmd", "", "cli.js"},
		{"missing entry script", "claude.cmd", cliPkgJSON, ""},
		{"not a shim extension", "claude.exe", cliPkgJSON, "cli.js"},
		{"unparseable package.json", "claude.cmd", `{not json`, "cli.js"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shim, _ := writeShimLayout(t, tc.shimName, pkgRel, tc.pkgJSON, tc.entry)
			if got, ok := findShimCLIJS(shim); ok {
				t.Fatalf("findShimCLIJS = %q, true; want no bypass", got)
			}
		})
	}
}
