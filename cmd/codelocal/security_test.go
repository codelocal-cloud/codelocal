package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurityUsageDocumentsOptionsAndReport(t *testing.T) {
	var output bytes.Buffer
	writeSecurityUsage(&output)
	for _, value := range []string{"--offline", "--engines", "--root", "--fix", "--no-setup", "security setup", "security report"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("help missing %q:\n%s", value, output.String())
		}
	}
}

func TestSecurityCommandRejectsUnknownEngine(t *testing.T) {
	err := securityCommand(context.Background(), []string{
		"--root", t.TempDir(),
		"--engines", "gitleaks,unknown",
		"--offline",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown security engine(s): unknown") {
		t.Fatalf("error = %v", err)
	}
}

func TestSecurityReportCommandRequiresLatestReport(t *testing.T) {
	root := t.TempDir()
	err := securityReportCommand([]string{"--root", root})
	if err == nil || !strings.Contains(err.Error(), "run codelocal security first") {
		t.Fatalf("error = %v", err)
	}

	path := filepath.Join(root, ".codelocal", "security", "reports", "latest.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := securityReportCommand([]string{"--root", root}); err != nil {
		t.Fatal(err)
	}
}

func TestSecuritySetupRejectsUnknownEngineBeforeDownload(t *testing.T) {
	err := securitySetupCommand(context.Background(), []string{"--engines", "unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown security engine(s): unknown") {
		t.Fatalf("error = %v", err)
	}
}

func TestSecurityReportCommandRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codelocal", "security", "reports", "latest.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	err := securityReportCommand([]string{"--root", root})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v", err)
	}
}
