package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/approval"
	"github.com/0xmarkhydra/codelocal/internal/clientupdate"
	"github.com/0xmarkhydra/codelocal/internal/deviceauth"
	"github.com/0xmarkhydra/codelocal/internal/identity"
	"github.com/0xmarkhydra/codelocal/internal/mcphub"
	codelocalruntime "github.com/0xmarkhydra/codelocal/internal/runtime"
	"github.com/0xmarkhydra/codelocal/internal/runtimecontrol"
	"github.com/0xmarkhydra/codelocal/internal/state"
	"github.com/0xmarkhydra/codelocal/internal/version"
	"github.com/0xmarkhydra/codelocal/internal/workspace"
)

const defaultCloud = "https://codelocal.cloud"

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func usage() {
	fmt.Print(`CodeLocal CLI

Usage:
  codelocal [command] [options]

Authentication:
  codelocal login [gateway]          Sign in and pair this machine
  codelocal login --force [gateway]  Sign in again / switch account
  codelocal logout [gateway]         Sign out this machine; keep local workspaces
  codelocal switch-account [gateway] Switch this machine to another account

Runtime:
  codelocal                          Start or attach to the machine runtime
  codelocal status                   Show account, pairing and runtime status
  codelocal stop                     Stop the machine runtime

Reset / uninstall:
  codelocal reset --all              Remove all local data; keep CodeLocal installed
  codelocal uninstall --all          Remove all local data and the global npm package
  Add --yes to either command for intentional non-interactive cleanup

Capabilities:
  codelocal setup                    Change Browser Automation and Computer Use choices
  codelocal doctor [project]         Inspect coding and automation readiness
  codelocal security [options]       Scan the current project for security issues
  codelocal security report          Show the latest local security report
  codelocal agent on [project]       Enable bounded Agent Mode for a workspace
  codelocal agent off [project]      Return that workspace to prompt mode
  codelocal agent status [project]   Show the effective local approval mode

Workspaces:
  codelocal .                        Authorize the current folder locally
  codelocal <project-path>           Authorize a project folder locally
  codelocal grant <project-path>     Authorize a project folder
  codelocal ungrant <path|id>        Remove local workspace authorization
  codelocal workspaces               List local authorized workspaces

Developer tools:
  codelocal approvals list           List remembered local approvals
  codelocal approvals revoke <id>    Revoke one remembered approval
  codelocal approvals reset          Forget remembered approvals for this workspace
  codelocal mcp ...                  Manage local MCP extensions

Compatibility / advanced:
  codelocal pair [gateway]           Re-pair this machine (same as login --force)

Options:
  codelocal --help, -h               Show this help
  codelocal --version, -v            Print version

MCP Hub:
  codelocal mcp add <name> -- <command> [args...]
  codelocal mcp add <name> --stdio <command> [--arg <arg> ...]
  codelocal mcp add <name> --url <https://server/mcp>
  codelocal mcp list [--json]
  codelocal mcp info <name>
  codelocal mcp probe <name>
  codelocal mcp search <query> [--server <name>]
  codelocal mcp remove <name> [--global|--workspace]

Examples:
  codelocal login
  codelocal .
  codelocal
  codelocal status
  codelocal logout
`)
}

func baseURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = first(os.Getenv("CODELOCAL_SERVER"), os.Getenv("SERVER_URL"), defaultCloud)
	}
	u, err := url.Parse(value)
	if err != nil {
		return strings.TrimRight(value, "/")
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func authBase(value string) string {
	if strings.TrimSpace(value) != "" {
		return baseURL(value)
	}
	if configured := first(os.Getenv("CODELOCAL_SERVER"), os.Getenv("SERVER_URL")); strings.TrimSpace(configured) != "" {
		return baseURL(configured)
	}
	if saved, err := identity.Load(""); err == nil && saved != nil && strings.TrimSpace(saved.ServerURL) != "" {
		return baseURL(saved.ServerURL)
	}
	return baseURL(defaultCloud)
}

func websocketBase(value string) string {
	u, _ := url.Parse(baseURL(value))
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/client"
	return u.String()
}
func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func openBrowser(target string) bool {
	if os.Getenv("CODELOCAL_NO_BROWSER") == "1" {
		return false
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start() == nil
}

func postJSON(ctx context.Context, target string, input any, headers map[string]string) (*http.Response, error) {
	raw, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return (&http.Client{Timeout: 15 * time.Second}).Do(req)
}
func credentialHeaders(c identity.Credential) map[string]string {
	return map[string]string{"X-CodeLocal-Credential-Id": c.CredentialID, "Authorization": "Device " + c.CredentialSecret}
}

func postCredentialJSON(ctx context.Context, target string, input any, credential identity.Credential) (*http.Response, error) {
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
	return (&http.Client{Timeout: 15 * time.Second}).Do(req)
}

func pair(ctx context.Context, server string) (identity.Credential, error) {
	base := baseURL(server)
	deviceID, deviceName := identity.Device()
	resp, err := postJSON(ctx, base+"/pair/start", map[string]any{"deviceId": deviceID, "deviceName": deviceName}, nil)
	if err != nil {
		return identity.Credential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return identity.Credential{}, fmt.Errorf("pair start failed (%d)", resp.StatusCode)
	}
	var pairing struct {
		PairingID      string `json:"pairingId"`
		Code           string `json:"code"`
		ExpiresAt      int64  `json:"expiresAt"`
		ApproveURL     string `json:"approveUrl"`
		RetrySafeClaim bool   `json:"retrySafeClaim"`
		DeviceSigning  bool   `json:"deviceSigning"`
	}
	if json.NewDecoder(resp.Body).Decode(&pairing) != nil {
		return identity.Credential{}, errors.New("invalid pairing response")
	}
	fmt.Printf("CodeLocal needs to pair this machine.\nPairing code: %s\nApprove: %s\n", pairing.Code, pairing.ApproveURL)
	if openBrowser(pairing.ApproveURL) {
		fmt.Println("Opened your default browser. Sign in to CodeLocal and approve this device.")
	}
	fmt.Println("Waiting for approval…")
	claimInput := map[string]any{"pairingId": pairing.PairingID, "code": pairing.Code}
	expectedCredentialID := ""
	expectedCredentialSecret := ""
	expectedDevicePublicKey := ""
	expectedDevicePrivateKey := ""
	claimDeadline := pairing.ExpiresAt
	if pairing.RetrySafeClaim {
		credentialIDHex, idErr := randomHex(16)
		if idErr != nil {
			return identity.Credential{}, fmt.Errorf("generate device credential id: %w", idErr)
		}
		secret, secretErr := randomHex(40)
		if secretErr != nil {
			return identity.Credential{}, fmt.Errorf("generate device credential secret: %w", secretErr)
		}
		expectedCredentialID = "cld_" + credentialIDHex
		expectedCredentialSecret = secret
		claimInput["credentialId"] = expectedCredentialID
		claimInput["credentialSecretHash"] = hashSecret(expectedCredentialSecret)
		if pairing.DeviceSigning {
			publicKey, privateKey, keyErr := deviceauth.GenerateKeyPair()
			if keyErr != nil {
				return identity.Credential{}, fmt.Errorf("generate device signing key: %w", keyErr)
			}
			expectedDevicePublicKey = publicKey
			expectedDevicePrivateKey = privateKey
			claimInput["devicePublicKey"] = expectedDevicePublicKey
		}
		// A successful claim may have committed just before the original pairing
		// expiry while its HTTP response was lost. Give idempotent retries a small
		// grace window without extending server-side approval validity.
		claimDeadline += int64((30 * time.Second).Milliseconds())
	}
	for time.Now().UnixMilli() < claimDeadline {
		select {
		case <-ctx.Done():
			return identity.Credential{}, ctx.Err()
		case <-time.After(time.Second):
		}
		claim, claimErr := postJSON(ctx, base+"/pair/claim", claimInput, nil)
		if claimErr != nil {
			continue
		}
		if claim.StatusCode >= 200 && claim.StatusCode < 300 {
			var c identity.Credential
			decodeErr := json.NewDecoder(claim.Body).Decode(&c)
			claim.Body.Close()
			if decodeErr != nil {
				return identity.Credential{}, decodeErr
			}
			if pairing.RetrySafeClaim {
				if c.CredentialID != expectedCredentialID {
					return identity.Credential{}, errors.New("pair claim returned an unexpected credential")
				}
				c.CredentialSecret = expectedCredentialSecret
				c.DevicePublicKey = expectedDevicePublicKey
				c.DevicePrivateKey = expectedDevicePrivateKey
			}
			c.ServerURL = websocketBase(base)
			c.CreatedAt = time.Now().UnixMilli()
			if err := identity.Save(c); err != nil {
				return identity.Credential{}, err
			}
			fmt.Printf("✓ Device paired: %s\n", c.DeviceName)
			return c, nil
		}
		if claim.StatusCode == http.StatusBadRequest {
			if _, signed := claimInput["devicePublicKey"]; signed {
				claim.Body.Close()
				delete(claimInput, "devicePublicKey")
				expectedDevicePublicKey = ""
				expectedDevicePrivateKey = ""
				continue
			}
		}
		claim.Body.Close()
		if claim.StatusCode != 409 && claim.StatusCode != 400 {
			slog.Warn("pair claim pending", "status", claim.StatusCode)
		}
	}
	return identity.Credential{}, errors.New("pairing expired before approval")
}

func validateCredential(ctx context.Context, base string, c identity.Credential) (bool, string, error) {
	resp, err := postCredentialJSON(ctx, baseURL(base)+"/api/client/auth/check", map[string]any{}, c)
	if err != nil {
		return false, "", fmt.Errorf("unable to verify CodeLocal credential: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return true, strings.TrimSpace(result.Email), nil
	}
	if resp.StatusCode == http.StatusUnauthorized && strings.TrimSpace(resp.Header.Get("X-CodeLocal-Auth-Check")) == "credential-v1" {
		return false, "", nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return false, "", fmt.Errorf("CodeLocal credential check is ambiguous on this gateway (%d); the stored credential was kept. Update the gateway or run `codelocal login --force` if you intentionally want to pair again", resp.StatusCode)
	}
	return false, "", fmt.Errorf("CodeLocal credential check failed (%d)", resp.StatusCode)
}
func resolveCredential(ctx context.Context, server string, onPhase func(string)) (identity.Credential, string, error) {
	base := authBase(server)
	onPhase("checking")
	saved, _ := identity.Load(websocketBase(base))
	if saved != nil {
		valid, email, checkErr := validateCredential(ctx, base, *saved)
		if checkErr != nil {
			return identity.Credential{}, base, checkErr
		}
		if valid {
			if email != "" && saved.Email != email {
				saved.Email = email
				_ = identity.Save(*saved)
			}
			return *saved, base, nil
		}
	}
	if saved != nil {
		fmt.Println("Stored CodeLocal credential is no longer valid or the gateway is incompatible. Pairing this machine again…")
		_ = identity.Delete()
	}
	onPhase("pairing")
	credential, err := pair(ctx, base)
	return credential, base, err
}

func stopRuntimeBestEffort(ctx context.Context) {
	stopCtx, cancelStop := context.WithTimeout(ctx, 2*time.Second)
	_, _ = runtimecontrol.Send(stopCtx, state.Dir(), "shutdown")
	cancelStop()
}

func revokeRemoteCredential(ctx context.Context, credential identity.Credential, fallbackServer string) (bool, error) {
	base := baseURL(credential.ServerURL)
	if strings.TrimSpace(credential.ServerURL) == "" {
		base = baseURL(fallbackServer)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		revokeCtx, cancelRevoke := context.WithTimeout(ctx, 3*time.Second)
		resp, err := postCredentialJSON(revokeCtx, base+"/api/client/auth/logout", map[string]any{}, credential)
		if err != nil {
			cancelRevoke()
			lastErr = err
		} else {
			status := resp.StatusCode
			resp.Body.Close()
			cancelRevoke()
			if (status >= 200 && status < 300) || status == http.StatusUnauthorized || status == http.StatusForbidden {
				return true, nil
			}
			lastErr = fmt.Errorf("CodeLocal device logout failed (%d)", status)
			if status != http.StatusTooManyRequests && status < 500 {
				return false, lastErr
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
			}
		}
	}
	return false, lastErr
}

func printSignedIn(credential identity.Credential) {
	if credential.Email != "" {
		fmt.Printf("✓ Signed in as %s on %s\n", credential.Email, credential.DeviceName)
		return
	}
	fmt.Printf("✓ Signed in to CodeLocal on this machine: %s\n", credential.DeviceName)
}

func login(ctx context.Context, server string, force bool) error {
	base := authBase(server)
	if !force {
		credential, _, err := resolveCredential(ctx, base, func(string) {})
		if err != nil {
			return err
		}
		printSignedIn(credential)
		fmt.Println("Run `codelocal` to start the machine runtime.")
		fmt.Println("To sign in again or switch accounts, run `codelocal login --force`.")
		return nil
	}

	previous, err := identity.Load("")
	if err != nil {
		return err
	}
	credential, err := pair(ctx, base)
	if err != nil {
		return err
	}
	stopRuntimeBestEffort(ctx)
	if previous != nil && previous.CredentialID != "" && previous.CredentialID != credential.CredentialID {
		if revoked, revokeErr := revokeRemoteCredential(ctx, *previous, server); revokeErr != nil || !revoked {
			fmt.Println("! New sign-in succeeded, but CodeLocal Cloud could not confirm revocation of the previous device credential. The new credential is active; check the old account dashboard and revoke the previous device if it is still active.")
		}
	}
	printSignedIn(credential)
	fmt.Println("Run `codelocal` to start the machine runtime.")
	fmt.Println("If you changed CodeLocal accounts, reconnect CodeLocal in your MCP client so OAuth uses the same account as this machine.")
	return nil
}

func logout(ctx context.Context, server string) error {
	credential, err := identity.Load("")
	if err != nil {
		return err
	}
	if credential == nil {
		fmt.Println("CodeLocal is already signed out on this machine.")
		return nil
	}

	stopRuntimeBestEffort(ctx)
	remoteRevoked, revokeErr := revokeRemoteCredential(ctx, *credential, server)
	if revokeErr != nil || !remoteRevoked {
		return fmt.Errorf("logout not completed: CodeLocal Cloud could not confirm device revocation; the runtime was stopped and the local credential was kept so you can retry safely")
	}
	if err := identity.Delete(); err != nil {
		return err
	}
	fmt.Println("✓ Signed out of CodeLocal on this machine.")
	fmt.Println("Authorized workspace folders were kept. Run `codelocal login` to sign in again.")
	return nil
}

func optionalGatewayArg(command string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one gateway URL", command)
	}
	if len(args) == 0 {
		return "", nil
	}
	if strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("unknown %s option: %s", command, args[0])
	}
	return args[0], nil
}

func loginArgs(args []string) (server string, force bool, err error) {
	for _, arg := range args {
		if arg == "--force" || arg == "-f" {
			force = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return "", false, fmt.Errorf("unknown login option: %s", arg)
		}
		if server != "" {
			return "", false, errors.New("login accepts at most one gateway URL")
		}
		server = arg
	}
	return server, force, nil
}

func printStartupUpdate(parent context.Context) {
	if !clientupdate.StartupCheckEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()

	type startupResult struct {
		version *clientupdate.Notice
		surface *clientupdate.SurfaceNotice
	}
	results := make(chan startupResult, 2)
	go func() {
		notice, _ := clientupdate.CheckStartup(ctx, clientupdate.StartupOptionsFromEnv(version.Version, state.Dir()))
		results <- startupResult{version: notice}
	}()
	go func() {
		notice, _ := clientupdate.CheckToolSurface(ctx, clientupdate.SurfaceOptionsForServer(authBase(""), state.Dir()))
		results <- startupResult{surface: notice}
	}()

	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.version != nil {
				fmt.Print(clientupdate.RenderCLI(*result.version))
			}
			if result.surface != nil {
				fmt.Print(clientupdate.RenderSurfaceCLI(*result.surface))
			}
		case <-ctx.Done():
			return
		}
	}
}

func runRuntime(parent context.Context, server string) error {
	dir := state.Dir()
	lease, err := runtimecontrol.Acquire(version.Version, dir)
	if err != nil {
		return err
	}
	if !lease.Acquired {
		summary := runtimecontrol.Summary(parent, dir)
		fmt.Println("✓ CodeLocal is already running on this machine.")
		if detail, ok := summary["detail"].(map[string]any); ok {
			fmt.Printf("  Status: %v\n", detail["phase"])
			if authorized, ok := detail["authorizedWorkspaces"].([]any); ok {
				fmt.Printf("  Workspaces: %d authorized\n", len(authorized))
			}
		}
		return nil
	}
	defer lease.Release()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	phase := "starting"
	var machine *codelocalruntime.Runtime
	control, err := runtimecontrol.Start(ctx, dir, lease.Record.InstanceID, func(callCtx context.Context, cmd runtimecontrol.Command) (any, error) {
		switch cmd.Type {
		case "status":
			status := map[string]any{"running": true, "pid": os.Getpid(), "phase": phase}
			if machine != nil {
				for k, v := range machine.Status() {
					status[k] = v
				}
			} else {
				status["authorizedWorkspaces"] = []any{}
				status["activeWorkspaces"] = []any{}
			}
			return status, nil
		case "reload":
			if machine == nil {
				return map[string]any{"reloaded": false, "pending": true, "phase": phase}, nil
			}
			items, err := machine.SyncRegistry(callCtx, true)
			if err != nil {
				return nil, err
			}
			return map[string]any{"reloaded": true, "authorizedWorkspaces": len(items), "phase": phase}, nil
		case "shutdown":
			phase = "stopping"
			cancel()
			return map[string]any{"stopping": true}, nil
		}
		return nil, errors.New("invalid runtime command")
	})
	if err != nil {
		return err
	}
	defer control.Close()
	credential, base, err := resolveCredential(ctx, server, func(next string) { phase = next })
	if err != nil {
		return err
	}
	phase = "connecting"
	machine = codelocalruntime.New(codelocalruntime.Options{BaseURL: base, Credential: credential, OnReady: func() { phase = "online" }})
	err = machine.Run(ctx)
	machine.Stop()
	phase = "stopping"
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func grant(path string) error {
	if path == "" {
		path = "."
	}
	entry, err := workspace.New().Grant(path, "")
	if err != nil {
		return err
	}
	fmt.Printf("✓ Workspace added: %s\n  ID: %s\n  Path: %s\n", entry.WorkspaceName, entry.WorkspaceID, entry.LocalPath)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	result, sendErr := runtimecontrol.Send(ctx, state.Dir(), "reload")
	if sendErr == nil {
		fmt.Printf("✓ Running CodeLocal detected; workspace sync requested: %v\n", result)
	} else {
		fmt.Println("Run `codelocal` when you want to make this machine available to connected MCP clients.")
	}
	return nil
}
func ungrant(identifier string) error {
	if identifier == "" {
		return errors.New("workspace path or ID required")
	}
	removed, err := workspace.New().Revoke(identifier)
	if err != nil {
		return err
	}
	if !removed {
		return errors.New("workspace not found")
	}
	fmt.Println("✓ Workspace authorization removed.")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, _ = runtimecontrol.Send(ctx, state.Dir(), "reload")
	return nil
}
func listWorkspaces() error {
	items, err := workspace.New().List()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("No local CodeLocal workspaces authorized.")
		return nil
	}
	for _, item := range items {
		fmt.Printf("%s\t%s\t%s\n", item.WorkspaceID, item.WorkspaceName, item.LocalPath)
	}
	return nil
}
func status() error {
	c, _ := identity.Load("")
	deviceID, deviceName := identity.Device()
	result := map[string]any{"version": version.Version, "device": map[string]any{"deviceId": deviceID, "deviceName": deviceName}, "paired": c != nil, "stateDir": state.Dir(), "runtime": runtimecontrol.Summary(context.Background(), state.Dir())}
	if c != nil {
		result["credential"] = map[string]any{"credentialId": c.CredentialID, "deviceId": c.DeviceID, "deviceName": c.DeviceName, "serverUrl": c.ServerURL, "createdAt": c.CreatedAt, "credentialSecret": "[REDACTED]"}
		if c.Email != "" {
			result["account"] = map[string]any{"email": c.Email}
		}
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(raw))
	return nil
}
func stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := runtimecontrol.Send(ctx, state.Dir(), "shutdown")
	if os.IsNotExist(err) {
		fmt.Println("CodeLocal is not running.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("✓ CodeLocal stopping: %v\n", result)
	return nil
}

func commandExists(name string) bool { _, err := exec.LookPath(name); return err == nil }
func doctor(path string) error {
	if path == "" {
		path = "."
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	markers := []string{"package.json", "pyproject.toml", "Cargo.toml", "go.mod", "pom.xml", "build.gradle", "CMakeLists.txt", "Package.swift", "pubspec.yaml"}
	detected := []string{}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			detected = append(detected, marker)
		}
	}
	fmt.Printf("\nCodeLocal doctor\nProject: %s\nDetected: %s\n\n", root, func() string {
		if len(detected) > 0 {
			return strings.Join(detected, ", ")
		}
		return "generic workspace"
	}())
	required := []string{"git", "rg"}
	optional := []string{"node", "npm", "rust-analyzer", "gopls", "clangd", "sourcekit-lsp", "dart"}
	for _, name := range append(required, optional...) {
		ok := commandExists(name)
		prefix := "·"
		if ok {
			prefix = "✓"
		}
		detail := ""
		if contains(optional, name) {
			detail = " — optional"
		}
		fmt.Printf("%s %s%s\n", prefix, name, detail)
	}
	return nil
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func agentModeCommand(args []string) error {
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	project := "."
	if len(args) > 1 {
		project = args[1]
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return err
	}
	if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("agent mode target must be a workspace directory")
	}
	workspaceID := workspace.IDForPath(abs)
	switch action {
	case "on", "enable":
		if err := approval.SetWorkspaceMode(workspaceID, approval.ModeAgent); err != nil {
			return err
		}
		fmt.Printf("✓ Agent Mode enabled for %s. Routine scoped actions can run without repeated prompts; critical actions still require fresh confirmation.\n", abs)
	case "off", "disable":
		if err := approval.SetWorkspaceMode(workspaceID, approval.ModePrompt); err != nil {
			return err
		}
		fmt.Printf("✓ Agent Mode disabled for %s. Approval mode is prompt.\n", abs)
	case "status":
		mode := approval.ResolveMode(workspaceID)
		fmt.Printf("Agent Mode: %s\nWorkspace: %s\nWorkspace ID: %s\n", mode, abs, workspaceID)
		if raw := strings.TrimSpace(os.Getenv("CODELOCAL_APPROVAL_MODE")); raw != "" {
			fmt.Printf("Source: CODELOCAL_APPROVAL_MODE=%s\n", approval.NormalizeMode(raw))
		} else {
			fmt.Println("Source: local workspace setting")
		}
	default:
		return errors.New("usage: codelocal agent <on|off|status> [project]")
	}
	return nil
}

func approvalsCommand(args []string) error {
	memory := approval.New()
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	workspaceKey := ""
	if cwd, err := filepath.EvalSymlinks("."); err == nil {
		workspaceKey = workspace.IDForPath(cwd)
	}
	switch action {
	case "list":
		items, err := memory.List("")
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(map[string]any{"approvals": items}, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "reset":
		count, err := memory.Reset(workspaceKey)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Revoked %d remembered approvals.\n", count)
		return nil
	case "revoke":
		if len(args) < 2 {
			return errors.New("usage: codelocal approvals revoke <id|actionKey>")
		}
		count, err := memory.Revoke(args[1], workspaceKey)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Revoked %d approvals.\n", count)
		return nil
	default:
		return errors.New("unknown approvals action")
	}
}

func optValues(args []string, name string) ([]string, error) {
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] != name {
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return nil, fmt.Errorf("%s requires a value", name)
		}
		out = append(out, args[i+1])
		i++
	}
	return out, nil
}
func optValue(args []string, name string) (string, error) {
	values, err := optValues(args, name)
	if err != nil {
		return "", err
	}
	if len(values) > 1 {
		return "", fmt.Errorf("%s may only be specified once", name)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}
func parseEnv(value string) (string, mcphub.EnvReference, error) {
	parts := strings.SplitN(value, "=", 2)
	target := strings.TrimSpace(parts[0])
	source := target
	if len(parts) == 2 {
		source = strings.TrimSpace(parts[1])
	}
	if target == "" || source == "" {
		return "", mcphub.EnvReference{}, errors.New("invalid --env")
	}
	return target, mcphub.EnvReference{Source: source}, nil
}
func parseHeader(value string) (string, mcphub.HeaderReference, error) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return "", mcphub.HeaderReference{}, errors.New("--header-env expects Header=ENV")
	}
	return strings.TrimSpace(parts[0]), mcphub.HeaderReference{Source: strings.TrimSpace(parts[1])}, nil
}
func mcpCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	action := args[0]
	if action == "install" {
		action = "add"
	}
	hub, err := mcphub.New(mustAbs("."), nil)
	if err != nil {
		return err
	}
	defer hub.Close()
	name := ""
	if len(args) > 1 {
		name = args[1]
	}
	rest := []string{}
	if len(args) > 2 {
		rest = args[2:]
	}
	switch action {
	case "list":
		servers, err := hub.List()
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(map[string]any{"servers": servers}, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "info":
		if name == "" {
			return errors.New("usage: codelocal mcp info <name>")
		}
		server, err := hub.Info(name)
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(server, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "probe":
		if name == "" {
			return errors.New("usage: codelocal mcp probe <name>")
		}
		result, err := hub.Probe(ctx, name, true)
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "search":
		query := strings.TrimSpace(strings.Join(args[1:], " "))
		server, _ := optValue(rest, "--server")
		if server != "" {
			query = strings.TrimSpace(strings.TrimSuffix(query, "--server "+server))
		}
		result, err := hub.Search(ctx, query, server, 8, false)
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "remove", "uninstall":
		if name == "" {
			return errors.New("usage: codelocal mcp remove <name>")
		}
		scope := ""
		if contains(rest, "--global") {
			scope = "global"
		}
		if contains(rest, "--workspace") {
			scope = "workspace"
		}
		result, err := hub.Remove(name, scope)
		if err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "add":
		if name == "" {
			return errors.New("usage: codelocal mcp add <name> ...")
		}
		separator := index(rest, "--")
		commandTail := []string{}
		options := rest
		if separator >= 0 {
			options = rest[:separator]
			commandTail = rest[separator+1:]
		}
		scope := "workspace"
		if contains(options, "--global") {
			scope = "global"
		}
		remoteURL, _ := optValue(options, "--url")
		stdio, _ := optValue(options, "--stdio")
		command := ""
		commandArgs := []string{}
		if len(commandTail) > 0 {
			command = commandTail[0]
			commandArgs = commandTail[1:]
		} else if stdio != "" {
			command = stdio
			commandArgs, _ = optValues(options, "--arg")
		}
		env := map[string]mcphub.EnvReference{}
		for _, value := range mustOpts(options, "--env") {
			k, v, parseErr := parseEnv(value)
			if parseErr != nil {
				return parseErr
			}
			env[k] = v
		}
		headers := map[string]mcphub.HeaderReference{}
		for _, value := range mustOpts(options, "--header-env") {
			k, v, parseErr := parseHeader(value)
			if parseErr != nil {
				return parseErr
			}
			headers[k] = v
		}
		if bearer, _ := optValue(options, "--bearer-env"); bearer != "" {
			headers["Authorization"] = mcphub.HeaderReference{Source: bearer, Prefix: "Bearer "}
		}
		cwd, _ := optValue(options, "--cwd")
		transport := "stdio"
		if remoteURL != "" {
			transport = "http"
		}
		config := mcphub.ServerConfig{Name: name, Enabled: true, Scope: scope, WorkspaceRoot: func() string {
			if scope == "workspace" {
				return mustAbs(".")
			}
			return ""
		}(), Transport: transport, Command: command, Args: commandArgs, CWD: cwd, Env: env, URL: remoteURL, Headers: headers}
		saved, err := hub.Add(config)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Installed MCP %s.\n", name)
		if !contains(options, "--no-probe") {
			probe, probeErr := hub.Probe(ctx, name, true)
			if probeErr != nil {
				fmt.Printf("! Installed, but probe failed: %v\n", probeErr)
			} else {
				raw, _ := json.MarshalIndent(probe, "", "  ")
				fmt.Println(string(raw))
			}
		}
		_ = saved
		return nil
	default:
		return fmt.Errorf("unknown MCP action: %s", action)
	}
}
func mustAbs(path string) string {
	abs, _ := filepath.Abs(path)
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return real
	}
	return abs
}
func mustOpts(args []string, name string) []string { values, _ := optValues(args, name); return values }
func index(values []string, target string) int {
	for i, v := range values {
		if v == target {
			return i
		}
	}
	return -1
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := os.Args[1:]
	if len(args) == 1 && contains([]string{"--version", "-v", "version"}, args[0]) {
		fmt.Println(version.Version)
		return
	}
	var err error
	if len(args) == 0 {
		printStartupUpdate(ctx)
		err = runRuntime(ctx, "")
	} else {
		switch args[0] {
		case "help", "--help", "-h":
			usage()
			return
		case ".":
			err = grant(".")
		case "grant", "add":
			path := "."
			if len(args) > 1 {
				path = args[1]
			}
			err = grant(path)
		case "ungrant", "remove":
			if len(args) < 2 {
				err = errors.New("workspace path or ID required")
			} else {
				err = ungrant(args[1])
			}
		case "workspaces":
			err = listWorkspaces()
		case "status":
			err = status()
		case "stop":
			err = stop()
		case "reset":
			err = resetCommand(ctx, args[1:])
		case "uninstall":
			err = uninstallCommand(ctx, args[1:])
		case "setup":
			if len(args) != 1 {
				err = errors.New("setup does not accept arguments")
			} else {
				err = rerunAutomationSetup()
			}
		case "pair":
			server, parseErr := optionalGatewayArg("pair", args[1:])
			if parseErr != nil {
				err = parseErr
			} else {
				err = login(ctx, server, true)
			}
		case "login":
			server, force, parseErr := loginArgs(args[1:])
			if parseErr != nil {
				err = parseErr
			} else {
				err = login(ctx, server, force)
			}
		case "logout", "signout":
			server, parseErr := optionalGatewayArg("logout", args[1:])
			if parseErr != nil {
				err = parseErr
			} else {
				err = logout(ctx, server)
			}
		case "switch-account", "switch":
			server, parseErr := optionalGatewayArg("switch-account", args[1:])
			if parseErr != nil {
				err = parseErr
			} else {
				err = login(ctx, server, true)
			}
		case "doctor":
			path := "."
			if len(args) > 1 {
				path = args[1]
			}
			err = doctor(path)
		case "security":
			err = securityCommand(ctx, args[1:])
		case "agent":
			err = agentModeCommand(args[1:])
		case "approvals":
			err = approvalsCommand(args[1:])
		case "mcp":
			err = mcpCommand(ctx, args[1:])
		default:
			if strings.HasPrefix(args[0], "-") {
				err = fmt.Errorf("unknown option: %s", args[0])
			} else {
				err = grant(args[0])
			}
		}
	}
	if err != nil {
		slog.Error("CodeLocal failed", "error", err)
		os.Exit(1)
	}
}
