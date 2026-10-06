package secscan

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagedManifestHasPinnedArtifacts(t *testing.T) {
	for _, artifact := range managedArtifacts {
		if artifact.Version == "" || !strings.HasPrefix(artifact.URL, "https://github.com/") {
			t.Fatalf("invalid managed artifact: %#v", artifact)
		}
		if len(artifact.SHA256) != 64 {
			t.Fatalf("%s %s checksum length = %d", artifact.Name, artifact.Version, len(artifact.SHA256))
		}
	}
	for _, name := range []string{"gitleaks", "trivy"} {
		if _, ok := managedArtifactFor(name, runtime.GOOS, runtime.GOARCH); !ok {
			// Some less common platforms remain deliberately external.
			t.Logf("no managed %s artifact for %s/%s", name, runtime.GOOS, runtime.GOARCH)
		}
	}
}

func TestManagedDownloadHostAllowlist(t *testing.T) {
	for _, host := range []string{"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		if !managedDownloadHost(host) {
			t.Fatalf("trusted host rejected: %s", host)
		}
	}
	for _, host := range []string{"github.com.example.test", "raw.githubusercontent.com", "example.com"} {
		if managedDownloadHost(host) {
			t.Fatalf("untrusted host accepted: %s", host)
		}
	}
}

func TestExtractManagedBinaryIgnoresTraversalEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name string
		data string
	}{
		{name: "../../not-gitleaks", data: "bad"},
		{name: "gitleaks", data: "expected-binary"},
	}
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := extractManagedBinary(path, managedArtifact{Name: "gitleaks", Archive: "tar.gz", BinaryName: "gitleaks"})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "expected-binary" {
		t.Fatalf("extracted = %q", raw)
	}
}

func TestValidateManagedArtifactRejectsUntrustedURL(t *testing.T) {
	err := validateManagedArtifact(managedArtifact{
		Name: "gitleaks", URL: "https://example.com/gitleaks.tar.gz",
		SHA256: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("expected untrusted URL rejection")
	}
}
