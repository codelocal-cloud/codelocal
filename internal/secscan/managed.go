package secscan

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/state"
)

const (
	managedDownloadLimit = int64(128 << 20)
	managedBinaryLimit   = int64(160 << 20)
)

type managedArtifact struct {
	Name       string
	Version    string
	GOOS       string
	GOARCH     string
	URL        string
	SHA256     string
	Archive    string
	BinaryName string
}

type ManagedInstallResult struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

var managedArtifacts = []managedArtifact{
	{Name: "gitleaks", Version: "8.30.1", GOOS: "darwin", GOARCH: "arm64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_arm64.tar.gz", SHA256: "b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5", Archive: "tar.gz", BinaryName: "gitleaks"},
	{Name: "gitleaks", Version: "8.30.1", GOOS: "darwin", GOARCH: "amd64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_x64.tar.gz", SHA256: "dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709", Archive: "tar.gz", BinaryName: "gitleaks"},
	{Name: "gitleaks", Version: "8.30.1", GOOS: "linux", GOARCH: "arm64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_arm64.tar.gz", SHA256: "e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080", Archive: "tar.gz", BinaryName: "gitleaks"},
	{Name: "gitleaks", Version: "8.30.1", GOOS: "linux", GOARCH: "amd64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz", SHA256: "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb", Archive: "tar.gz", BinaryName: "gitleaks"},
	{Name: "gitleaks", Version: "8.30.1", GOOS: "windows", GOARCH: "amd64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_windows_x64.zip", SHA256: "d29144deff3a68aa93ced33dddf84b7fdc26070add4aa0f4513094c8332afc4e", Archive: "zip", BinaryName: "gitleaks.exe"},
	{Name: "gitleaks", Version: "8.30.1", GOOS: "windows", GOARCH: "arm64", URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_windows_arm64.zip", SHA256: "b95f5e4f5c425cedca7ee203d9afd29597e692c4924a12ed42f970537c72cc0f", Archive: "zip", BinaryName: "gitleaks.exe"},

	{Name: "trivy", Version: "0.74.0", GOOS: "darwin", GOARCH: "arm64", URL: "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_macOS-ARM64.tar.gz", SHA256: "1caada5e0e2091909357c7525d3aa76f4b660b13821bc143b190c7483e31cc11", Archive: "tar.gz", BinaryName: "trivy"},
	{Name: "trivy", Version: "0.74.0", GOOS: "darwin", GOARCH: "amd64", URL: "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_macOS-64bit.tar.gz", SHA256: "472816f6888dda689d075c30254d4210b4d1035acf365aa72332f584c2f60485", Archive: "tar.gz", BinaryName: "trivy"},
	{Name: "trivy", Version: "0.74.0", GOOS: "linux", GOARCH: "arm64", URL: "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_Linux-ARM64.tar.gz", SHA256: "b94ce1976bbf3c15b514b605ee88be7c6d94a29be2302847ff01cb794d47aad5", Archive: "tar.gz", BinaryName: "trivy"},
	{Name: "trivy", Version: "0.74.0", GOOS: "linux", GOARCH: "amd64", URL: "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_Linux-64bit.tar.gz", SHA256: "2ae6fe3ee734b7fdf11335663e18c75ea12dccc76062f09f164a3b0f8be4371a", Archive: "tar.gz", BinaryName: "trivy"},
	{Name: "trivy", Version: "0.74.0", GOOS: "windows", GOARCH: "amd64", URL: "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_windows-64bit.zip", SHA256: "94c40e0696e4b907a74b7b2e1438d5d72ebaca83115817407f568a002d520842", Archive: "zip", BinaryName: "trivy.exe"},
}

func ManagedScannerVersion(name string) string {
	artifact, ok := managedArtifactFor(name, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return ""
	}
	return artifact.Version
}

func ManagedScannerPath(name string) string {
	artifact, ok := managedArtifactFor(name, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return ""
	}
	return managedScannerPath(artifact)
}

func scannerExecutable(name string) (string, error) {
	envKey := "CODELOCAL_SECURITY_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_PATH"
	if configured := strings.TrimSpace(os.Getenv(envKey)); configured != "" {
		info, err := os.Stat(configured)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("%w: %s points to an invalid executable", ErrUnavailable, envKey)
		}
		return configured, nil
	}
	if managed := ManagedScannerPath(name); managed != "" {
		if info, err := os.Stat(managed); err == nil && !info.IsDir() {
			return managed, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%w: %s is not installed", ErrUnavailable, name)
}

func ManagedScannerInstalled(name string) bool {
	_, err := scannerExecutable(name)
	return err == nil
}

func SetupManagedScanners(ctx context.Context, names []string, progress io.Writer) ([]ManagedInstallResult, error) {
	if len(names) == 0 {
		names = []string{"gitleaks", "trivy"}
	}
	seen := map[string]struct{}{}
	results := make([]ManagedInstallResult, 0, len(names))
	var errs []error
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		if name == "semgrep" {
			err := fmt.Errorf("%w: Semgrep managed install is intentionally disabled until its Python dependency chain can be checksum-pinned; install Semgrep separately or set CODELOCAL_SECURITY_SEMGREP_PATH", ErrUnavailable)
			results = append(results, ManagedInstallResult{
				Name: "semgrep", Status: "external", Error: err.Error(),
			})
			errs = append(errs, err)
			continue
		}
		artifact, ok := managedArtifactFor(name, runtime.GOOS, runtime.GOARCH)
		if !ok {
			err := fmt.Errorf("%w: no pinned %s artifact for %s/%s", ErrUnavailable, name, runtime.GOOS, runtime.GOARCH)
			results = append(results, ManagedInstallResult{Name: name, Status: "unsupported", Error: err.Error()})
			errs = append(errs, err)
			continue
		}
		path := managedScannerPath(artifact)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			results = append(results, ManagedInstallResult{Name: name, Version: artifact.Version, Path: path, Status: "ready"})
			continue
		}
		if progress != nil {
			fmt.Fprintf(progress, "Preparing %-10s v%s (%s/%s)\n", name, artifact.Version, artifact.GOOS, artifact.GOARCH)
		}
		if err := installManagedArtifact(ctx, artifact); err != nil {
			results = append(results, ManagedInstallResult{Name: name, Version: artifact.Version, Status: "error", Error: err.Error()})
			errs = append(errs, err)
			continue
		}
		results = append(results, ManagedInstallResult{Name: name, Version: artifact.Version, Path: path, Status: "installed"})
	}
	return results, errors.Join(errs...)
}

func managedArtifactFor(name, goos, goarch string) (managedArtifact, bool) {
	for _, artifact := range managedArtifacts {
		if artifact.Name == name && artifact.GOOS == goos && artifact.GOARCH == goarch {
			return artifact, true
		}
	}
	return managedArtifact{}, false
}

func managedScannerPath(artifact managedArtifact) string {
	return filepath.Join(state.Dir(), "security", "tools", artifact.Name, artifact.Version, artifact.BinaryName)
}

func installManagedArtifact(ctx context.Context, artifact managedArtifact) error {
	if err := validateManagedArtifact(artifact); err != nil {
		return err
	}
	target := managedScannerPath(artifact)
	if err := state.EnsurePrivateDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("prepare managed scanner directory: %w", err)
	}
	archive, err := downloadManagedArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	payload, err := extractManagedBinary(archive, artifact)
	if err != nil {
		return err
	}
	defer payload.Close()

	temp, err := os.CreateTemp(filepath.Dir(target), "."+artifact.Name+"-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o700); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.Copy(temp, io.LimitReader(payload, managedBinaryLimit+1)); err != nil {
		temp.Close()
		return err
	}
	info, err := temp.Stat()
	if err != nil {
		temp.Close()
		return err
	}
	if info.Size() > managedBinaryLimit {
		temp.Close()
		return fmt.Errorf("managed %s binary exceeds size limit", artifact.Name)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(target)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedArtifact(artifact managedArtifact) error {
	parsed, err := url.Parse(artifact.URL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" {
		return fmt.Errorf("managed scanner URL must be an official GitHub HTTPS release URL")
	}
	if len(artifact.SHA256) != 64 {
		return fmt.Errorf("managed scanner checksum is invalid")
	}
	return nil
}

func downloadManagedArtifact(ctx context.Context, artifact managedArtifact) (string, error) {
	dir := filepath.Join(state.Dir(), "security", "downloads")
	if err := state.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: 3 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return errors.New("too many scanner download redirects")
			}
			if req.URL.Scheme != "https" || !managedDownloadHost(req.URL.Hostname()) {
				return fmt.Errorf("scanner download redirected to untrusted host %q", req.URL.Hostname())
			}
			return nil
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", artifact.Name, response.StatusCode)
	}
	if response.ContentLength > managedDownloadLimit {
		return "", fmt.Errorf("download %s: archive exceeds size limit", artifact.Name)
	}
	file, err := os.CreateTemp(dir, "."+artifact.Name+"-*.download")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, managedDownloadLimit+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact.Name, err)
	}
	if written > managedDownloadLimit {
		return "", fmt.Errorf("download %s: archive exceeds size limit", artifact.Name)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, artifact.SHA256) {
		return "", fmt.Errorf("download %s: checksum mismatch (got %s)", artifact.Name, got)
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func managedDownloadHost(host string) bool {
	switch strings.ToLower(host) {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

type managedBinaryReader struct {
	io.Reader
	closers []io.Closer
}

func (reader *managedBinaryReader) Close() error {
	var errs []error
	for index := len(reader.closers) - 1; index >= 0; index-- {
		errs = append(errs, reader.closers[index].Close())
	}
	return errors.Join(errs...)
}

func extractManagedBinary(path string, artifact managedArtifact) (io.ReadCloser, error) {
	switch artifact.Archive {
	case "tar.gz":
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, err
		}
		tarReader := tar.NewReader(gzipReader)
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				gzipReader.Close()
				file.Close()
				return nil, fmt.Errorf("%s archive does not contain %s", artifact.Name, artifact.BinaryName)
			}
			if err != nil {
				gzipReader.Close()
				file.Close()
				return nil, err
			}
			if header.Typeflag != tar.TypeReg || filepath.Base(filepath.Clean(header.Name)) != artifact.BinaryName {
				continue
			}
			if header.Size < 0 || header.Size > managedBinaryLimit {
				gzipReader.Close()
				file.Close()
				return nil, fmt.Errorf("%s binary exceeds size limit", artifact.Name)
			}
			return &managedBinaryReader{
				Reader:  io.LimitReader(tarReader, managedBinaryLimit+1),
				closers: []io.Closer{gzipReader, file},
			}, nil
		}
	case "zip":
		archive, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() || entry.Mode()&os.ModeSymlink != 0 || filepath.Base(filepath.Clean(entry.Name)) != artifact.BinaryName {
				continue
			}
			if int64(entry.UncompressedSize64) > managedBinaryLimit {
				archive.Close()
				return nil, fmt.Errorf("%s binary exceeds size limit", artifact.Name)
			}
			file, err := entry.Open()
			if err != nil {
				archive.Close()
				return nil, err
			}
			return &managedBinaryReader{Reader: file, closers: []io.Closer{file, archive}}, nil
		}
		archive.Close()
		return nil, fmt.Errorf("%s archive does not contain %s", artifact.Name, artifact.BinaryName)
	default:
		return nil, fmt.Errorf("unsupported managed scanner archive %q", artifact.Archive)
	}
}
