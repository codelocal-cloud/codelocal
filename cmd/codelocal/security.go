package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/deviceauth"
	"github.com/0xmarkhydra/codelocal/internal/identity"
	"github.com/0xmarkhydra/codelocal/internal/secscan"
)

func securityCommand(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "report" {
		return securityReportCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "setup" {
		return securitySetupCommand(ctx, args[1:])
	}
	flags := flag.NewFlagSet("security", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() { writeSecurityUsage(os.Stdout) }
	offline := flags.Bool("offline", false, "do not contact the CodeLocal AI service")
	engines := flags.String("engines", "", "comma-separated engines: gitleaks,trivy,semgrep")
	root := flags.String("root", ".", "project root to scan")
	noSetup := flags.Bool("no-setup", false, "do not install missing managed scanners")
	fixPlan := flags.Bool("fix", false, "write a remediation plan without modifying source files")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("security does not accept positional arguments; use --root <path>")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	selected := splitCSV(*engines)
	if err := secscan.ValidateEngineNames(selected); err != nil {
		return err
	}

	fmt.Printf("🔐 CodeLocal Security\n\nProject: %s\nMode: %s\n\n", abs, securityMode(*offline))
	if !*offline && !*noSetup {
		setupResults, setupErr := secscan.SetupManagedScanners(ctx, selected, os.Stdout)
		printSecuritySetupResults(setupResults, false)
		if setupErr != nil {
			fmt.Printf("Scanner setup  warning (%v)\n", setupErr)
		}
	}
	report, scanErr := secscan.Run(ctx, secscan.Options{Root: abs, Offline: *offline, Engines: selected})
	if report.ScanID != "" {
		if err := secscan.CompareWithLatest(abs, &report); err != nil {
			fmt.Printf("Comparison    unavailable (%v)\n", err)
			secscan.CompareReports(&report, nil)
		}
	}
	if *offline && report.ScanID != "" {
		report.AI.Status = "offline"
	}

	// Even a partial scan should leave an auditable local report.
	var partial secscan.WrittenReports
	if report.ScanID != "" {
		partial, err = secscan.WriteReports(abs, report)
		if err != nil {
			return err
		}
	}
	if scanErr != nil {
		printSecurityEngines(report)
		if partial.JSON != "" {
			fmt.Printf("\nReport:\n  %s\n  %s\n  %s\n",
				relativeForDisplay(abs, partial.JSON),
				relativeForDisplay(abs, partial.Markdown),
				relativeForDisplay(abs, partial.SARIF))
		}
		return scanErr
	}

	if *offline {
		report.AI.Status = "offline"
	} else if len(report.Findings) == 0 {
		report.AI.Status = "not-needed"
	} else {
		fmt.Println("AI review     analyzing sanitized findings...")
		updated, err := securityAIReview(ctx, report)
		if err != nil {
			report.AI.Status = "error"
			fmt.Printf("AI review     unavailable (%v)\n", err)
		} else {
			report = updated
			fmt.Println("AI review     complete")
		}
	}

	written, err := secscan.WriteReports(abs, report)
	if err != nil {
		return err
	}
	var fixWritten secscan.WrittenFixPlan
	if *fixPlan {
		fixWritten, err = secscan.WriteFixPlan(abs, report)
		if err != nil {
			return err
		}
	}

	printSecurityEngines(report)
	fmt.Printf("\nCritical %d  High %d  Medium %d  Low %d  Total %d\n",
		report.Summary.Critical, report.Summary.High, report.Summary.Medium, report.Summary.Low, report.Summary.Total)
	fmt.Printf("AI status: %s\n", report.AI.Status)
	fmt.Printf("\nReport:\n  %s\n  %s\n  %s\n",
		relativeForDisplay(abs, written.JSON), relativeForDisplay(abs, written.Markdown), relativeForDisplay(abs, written.SARIF))
	if fixWritten.Markdown != "" {
		fmt.Printf("\nFix plan (advisory; source unchanged):\n  %s\n  %s\n",
			relativeForDisplay(abs, fixWritten.JSON), relativeForDisplay(abs, fixWritten.Markdown))
	}
	return nil
}

func securitySetupCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("security setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() { writeSecuritySetupUsage(os.Stdout) }
	engines := flags.String("engines", "gitleaks,trivy", "comma-separated managed scanners")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("security setup does not accept positional arguments")
	}
	selected := splitCSV(*engines)
	if err := secscan.ValidateEngineNames(selected); err != nil {
		return err
	}
	results, err := secscan.SetupManagedScanners(ctx, selected, os.Stdout)
	printSecuritySetupResults(results, true)
	return err
}

func printSecuritySetupResults(results []secscan.ManagedInstallResult, verbose bool) {
	for _, result := range results {
		if !verbose && result.Status == "ready" {
			continue
		}
		fmt.Printf("%-12s setup=%s", result.Name, result.Status)
		if result.Version != "" {
			fmt.Printf(" version=%s", result.Version)
		}
		if result.Error != "" {
			fmt.Printf(" — %s", result.Error)
		}
		fmt.Println()
	}
}

func securityReportCommand(args []string) error {
	flags := flag.NewFlagSet("security report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() { writeSecurityReportUsage(os.Stdout) }
	root := flags.String("root", ".", "project root containing the report")
	openReport := flags.Bool("open", false, "open latest.md with the system application")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("security report does not accept positional arguments; use --root <path>")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	projectRoot, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	defer projectRoot.Close()
	reportPath := filepath.Join(".codelocal", "security", "reports", "latest.md")
	info, err := projectRoot.Lstat(reportPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no security report found at %s; run codelocal security first", filepath.Join(abs, reportPath))
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("security report is not a regular file: %s", filepath.Join(abs, reportPath))
	}
	content, err := projectRoot.ReadFile(reportPath)
	if err != nil {
		return err
	}
	path := filepath.Join(abs, reportPath)
	fmt.Print(string(content))
	if len(content) > 0 && content[len(content)-1] != '\n' {
		fmt.Println()
	}
	fmt.Printf("\nReport: %s\n", relativeForDisplay(abs, path))
	if *openReport && !openBrowser(path) {
		return fmt.Errorf("could not open security report: %s", path)
	}
	return nil
}

func writeSecurityUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  codelocal security [--offline] [--engines <list>] [--root <path>] [--fix] [--no-setup]
  codelocal security setup [--engines <list>]
  codelocal security report [--root <path>] [--open]

Options:
  --offline          Disable CodeLocal AI review and scanner network access
  --engines <list>   Comma-separated scanners: gitleaks,trivy,semgrep
  --root <path>      Project root to scan (default ".")
  --fix              Write fix-plan.json/md; does not modify source files
  --no-setup         Do not install missing managed scanners
  --help, -h         Show this help
`)
}

func writeSecuritySetupUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  codelocal security setup [--engines <list>]

Options:
  --engines <list>   Managed scanners to prepare (default "gitleaks,trivy")
  --help, -h         Show this help

Gitleaks and Trivy are installed from pinned release artifacts after SHA-256 verification.
Semgrep remains externally managed until its Python dependency chain can be checksum-pinned.
`)
}

func writeSecurityReportUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  codelocal security report [--root <path>] [--open]

Options:
  --root <path>   Project root containing the report (default ".")
  --open          Open latest.md with the system application
  --help, -h      Show this help
`)
}

func securityAIReview(ctx context.Context, report secscan.Report) (secscan.Report, error) {
	credential, err := identity.Load("")
	if err != nil {
		return report, fmt.Errorf("load device credential: %w", err)
	}
	if credential == nil || strings.TrimSpace(credential.CredentialID) == "" {
		return report, errors.New("CodeLocal login required for AI review")
	}

	target := strings.TrimRight(baseURL(credential.ServerURL), "/") + "/api/client/security/analyze"
	payload := secscan.BuildAIPayload(report)
	response, err := postSecurityAIJSON(ctx, target, payload, *credential)
	if err != nil {
		return report, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = response.Status
		}
		return report, fmt.Errorf("security AI server returned %d: %s", response.StatusCode, message)
	}

	var aiResponse secscan.AIResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&aiResponse); err != nil {
		return report, fmt.Errorf("decode security AI response: %w", err)
	}
	secscan.ApplyAIResponse(&report, aiResponse, time.Now())
	return report, nil
}

func postSecurityAIJSON(ctx context.Context, target string, input any, credential identity.Credential) (*http.Response, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range credentialHeaders(credential) {
		req.Header.Set(key, value)
	}
	if err := deviceauth.SignRequest(req, raw, credential.DevicePrivateKey, time.Now()); err != nil {
		return nil, err
	}
	return (&http.Client{Timeout: 90 * time.Second}).Do(req)
}

func printSecurityEngines(report secscan.Report) {
	for _, engine := range report.Engines {
		state := engine.Status
		if state == "ok" {
			state = "ready"
		} else if state == "unavailable" {
			state = "unavailable"
		}
		fmt.Printf("%-12s %s", engine.Name, state)
		if engine.Findings > 0 {
			fmt.Printf(" (%d findings)", engine.Findings)
		}
		if engine.Error != "" {
			fmt.Printf(" — %s", engine.Error)
		}
		fmt.Println()
	}
}

func securityMode(offline bool) string {
	if offline {
		return "offline"
	}
	return "local scan + CodeLocal AI review"
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func relativeForDisplay(root, value string) string {
	if rel, err := filepath.Rel(root, value); err == nil {
		return filepath.ToSlash(rel)
	}
	return value
}
