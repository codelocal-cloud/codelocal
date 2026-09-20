package localclient

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/editing"
	"github.com/0xmarkhydra/codelocal/internal/localfs"
	"github.com/0xmarkhydra/codelocal/internal/repository"
	"github.com/0xmarkhydra/codelocal/internal/taskexecution"
)

const (
	privateTaskExecutionID    = "__codelocalTaskId"
	privateTaskExecutionOwner = "__codelocalTaskOwner"
)

type taskExecutionTarget struct {
	TaskID         string
	OwnerID        string
	Repository     repository.Checkout
	Binding        taskexecution.RepositoryBinding
	Provider       taskexecution.Provider
	FS             *localfs.FS
	RepositoryPath string
	Active         bool
}

func taskExecutionIdentity(args map[string]any, opts HandleOptions) (string, string) {
	taskID := strings.TrimSpace(asString(args[privateTaskExecutionID]))
	ownerID := strings.TrimSpace(asString(args[privateTaskExecutionOwner]))
	if ownerID == "" {
		ownerID = strings.TrimSpace(opts.SessionID)
	}
	if ownerID == "" {
		ownerID = taskID
	}
	return taskID, ownerID
}

func taskBindingForRepository(bundle taskexecution.Bundle, repo repository.Checkout) (taskexecution.RepositoryBinding, bool) {
	for _, binding := range bundle.RepositoryBindings {
		if binding.RepositoryID == repo.ID && binding.RepositoryPath == repo.RelativePath {
			return binding, true
		}
	}
	return taskexecution.RepositoryBinding{}, false
}

func taskExecutionMetadata(target taskExecutionTarget) map[string]any {
	if !target.Active {
		return nil
	}
	return map[string]any{
		"taskId":         target.TaskID,
		"provider":       string(target.Provider),
		"repositoryId":   target.Repository.ID,
		"repositoryPath": target.Repository.RelativePath,
	}
}

func attachTaskExecutionMetadata(result map[string]any, target taskExecutionTarget, workspacePath string) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	if strings.TrimSpace(workspacePath) != "" {
		result["path"] = filepath.ToSlash(filepath.Clean(workspacePath))
	}
	if metadata := taskExecutionMetadata(target); metadata != nil {
		result["taskExecution"] = metadata
	}
	return result
}

func (e *Engine) taskExecutionTargetForPath(ctx context.Context, args map[string]any, opts HandleOptions, workspacePath string, prepare bool) (taskExecutionTarget, error) {
	taskID, ownerID := taskExecutionIdentity(args, opts)
	if taskID == "" || e.TaskExecutions == nil {
		return taskExecutionTarget{FS: e.FS, RepositoryPath: workspacePath}, nil
	}
	if err := safeGitPath(workspacePath); err != nil {
		return taskExecutionTarget{}, err
	}
	repo, repoPath, err := e.Repositories.ResolvePath(workspacePath)
	if err != nil {
		target := taskExecutionTarget{TaskID: taskID, OwnerID: ownerID, FS: e.FS, RepositoryPath: workspacePath}
		if e.TaskExecutionProvider() == taskexecution.ProviderActiveCheckout {
			target.Provider = taskexecution.ProviderActiveCheckout
			return target, nil
		}
		if prepare {
			return taskExecutionTarget{}, errors.New("task execution mutation requires a path owned by a discovered Git repository")
		}
		return target, nil
	}
	bundle, ok, err := e.TaskExecutions.Store.Get(e.WorkspaceKey, taskID)
	if err != nil {
		return taskExecutionTarget{}, err
	}
	if prepare {
		bundle, err = e.TaskExecutions.Ensure(ctx, taskexecution.PrepareRequest{
			TaskID: taskID, WorkspaceID: e.WorkspaceID, WorkspaceKey: e.WorkspaceKey,
			Repositories: []repository.Checkout{repo},
		}, e.TaskExecutionProvider(), ownerID, 2*time.Minute)
		if err != nil {
			return taskExecutionTarget{}, err
		}
		ok = true
	}
	if !ok {
		return taskExecutionTarget{TaskID: taskID, OwnerID: ownerID, Repository: repo, FS: e.FS, RepositoryPath: workspacePath}, nil
	}
	binding, bound := taskBindingForRepository(bundle, repo)
	if !bound {
		return taskExecutionTarget{TaskID: taskID, OwnerID: ownerID, Repository: repo, FS: e.FS, RepositoryPath: workspacePath}, nil
	}
	fs, err := localfs.New(binding.LocalPath)
	if err != nil {
		return taskExecutionTarget{}, err
	}
	return taskExecutionTarget{TaskID: taskID, OwnerID: ownerID, Repository: repo, Binding: binding, Provider: bundle.Provider, FS: fs, RepositoryPath: repoPath, Active: true}, nil
}

func (e *Engine) taskReadFile(ctx context.Context, args map[string]any, opts HandleOptions, path string, startLine, endLine int) (map[string]any, error) {
	target, err := e.taskExecutionTargetForPath(ctx, args, opts, path, false)
	if err != nil {
		return nil, err
	}
	result, err := target.FS.Read(target.RepositoryPath, startLine, endLine)
	if err != nil {
		return nil, err
	}
	return attachTaskExecutionMetadata(result, target, path), nil
}

func (e *Engine) taskReadMany(ctx context.Context, args map[string]any, opts HandleOptions, paths []string) (map[string]any, error) {
	files := make([]map[string]any, 0, len(paths))
	var total int64
	for _, path := range paths {
		file, err := e.taskReadFile(ctx, args, opts, path, 0, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		switch value := file["size"].(type) {
		case int64:
			total += value
		case int:
			total += int64(value)
		case float64:
			total += int64(value)
		}
		if total > 8*1024*1024 {
			return nil, errors.New("batch exceeds configured read limit")
		}
	}
	return map[string]any{"files": files, "totalBytes": total}, nil
}

func (e *Engine) taskWriteFile(ctx context.Context, args map[string]any, opts HandleOptions, path, content, expectedHash string) (map[string]any, error) {
	target, err := e.taskExecutionTargetForPath(ctx, args, opts, path, true)
	if err != nil {
		return nil, err
	}
	result, err := target.FS.Write(target.RepositoryPath, content, expectedHash)
	if err != nil {
		return nil, err
	}
	return attachTaskExecutionMetadata(result, target, path), nil
}

func (e *Engine) taskExactEdit(ctx context.Context, args map[string]any, opts HandleOptions, path, oldText, newText string, replaceAll bool, expectedHash string) (map[string]any, error) {
	target, err := e.taskExecutionTargetForPath(ctx, args, opts, path, true)
	if err != nil {
		return nil, err
	}
	result, err := target.FS.ExactEdit(target.RepositoryPath, oldText, newText, replaceAll, expectedHash)
	if err != nil {
		return nil, err
	}
	return attachTaskExecutionMetadata(result, target, path), nil
}

func (e *Engine) taskApplyEdits(ctx context.Context, args map[string]any, opts HandleOptions, files []editing.FileEdit) (map[string]any, error) {
	if len(files) == 0 {
		return map[string]any{"files": []map[string]any{}}, nil
	}
	first, err := e.taskExecutionTargetForPath(ctx, args, opts, files[0].Path, true)
	if err != nil {
		return nil, err
	}
	translated := make([]editing.FileEdit, 0, len(files))
	workspacePaths := make([]string, 0, len(files))
	for _, file := range files {
		target, err := e.taskExecutionTargetForPath(ctx, args, opts, file.Path, true)
		if err != nil {
			return nil, err
		}
		if target.Repository.ID != first.Repository.ID || target.Repository.RelativePath != first.Repository.RelativePath {
			return nil, errors.New("task execution apply_edits must target one repository per call; split cross-repository edits into separate calls")
		}
		workspacePaths = append(workspacePaths, file.Path)
		file.Path = target.RepositoryPath
		translated = append(translated, file)
	}
	result, err := editing.New(first.FS).Apply(translated)
	if err != nil {
		return nil, err
	}
	if values, ok := result["files"].([]map[string]any); ok {
		for index := range values {
			if index < len(workspacePaths) {
				values[index]["path"] = filepath.ToSlash(filepath.Clean(workspacePaths[index]))
			}
		}
	}
	result["taskExecution"] = taskExecutionMetadata(first)
	return result, nil
}

func normalizeTaskPatchPayload(patch string) string {
	// Some clients serialize the unified patch as one JSON string containing
	// literal backslash-n sequences. Only decode that transport artifact when
	// the payload has no actual line breaks, so source content remains intact.
	if !strings.Contains(patch, "\n") && strings.Contains(patch, `\n`) {
		return strings.ReplaceAll(patch, `\n`, "\n")
	}
	return patch
}

func taskPatchPaths(patch string) []string {
	seen, paths := map[string]struct{}{}, []string{}
	patch = normalizeTaskPatchPayload(patch)
	for _, line := range strings.Split(patch, "\n") {
		candidate := ""
		switch {
		case strings.HasPrefix(line, "diff --git a/"):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				candidate = strings.TrimPrefix(fields[3], "b/")
			}
		case strings.HasPrefix(line, "+++ b/"):
			candidate = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")), "b/")
		}
		candidate = filepath.ToSlash(filepath.Clean(strings.TrimSpace(candidate)))
		if candidate == "" || candidate == "." || candidate == "/dev/null" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
	}
	return paths
}

func rewritePatchHeaderPath(line, repoPath string) string {
	if repoPath == "." || repoPath == "" {
		return line
	}
	for _, prefix := range []string{"a/", "b/"} {
		line = strings.ReplaceAll(line, prefix+repoPath+"/", prefix)
	}
	for _, prefix := range []string{"--- ", "+++ ", "rename from ", "rename to ", "copy from ", "copy to "} {
		line = strings.Replace(line, prefix+repoPath+"/", prefix, 1)
	}
	return line
}

func rewritePatchForRepository(patch, repoPath string) string {
	lines := strings.Split(patch, "\n")
	for index := range lines {
		if strings.HasPrefix(lines[index], "diff --git ") || strings.HasPrefix(lines[index], "--- ") || strings.HasPrefix(lines[index], "+++ ") || strings.HasPrefix(lines[index], "rename from ") || strings.HasPrefix(lines[index], "rename to ") || strings.HasPrefix(lines[index], "copy from ") || strings.HasPrefix(lines[index], "copy to ") {
			lines[index] = rewritePatchHeaderPath(lines[index], repoPath)
		}
	}
	return strings.Join(lines, "\n")
}

func (e *Engine) taskApplyPatch(ctx context.Context, args map[string]any, opts HandleOptions, patch string) (map[string]any, error) {
	paths := taskPatchPaths(patch)
	if len(paths) == 0 {
		return nil, errors.New("patch does not contain a routable workspace path")
	}
	first, err := e.taskExecutionTargetForPath(ctx, args, opts, paths[0], true)
	if err != nil {
		return nil, err
	}
	for _, path := range paths[1:] {
		target, err := e.taskExecutionTargetForPath(ctx, args, opts, path, true)
		if err != nil {
			return nil, err
		}
		if target.Repository.ID != first.Repository.ID || target.Repository.RelativePath != first.Repository.RelativePath {
			return nil, errors.New("task execution apply_patch must target one repository per call; split cross-repository patches")
		}
	}
	result, err := editing.New(first.FS).ApplyPatch(rewritePatchForRepository(patch, first.Repository.RelativePath))
	if err != nil {
		return nil, err
	}
	result["taskExecution"] = taskExecutionMetadata(first)
	return result, nil
}

func taskFormatter(root, relative string) (string, []string) {
	ext := strings.ToLower(filepath.Ext(relative))
	switch ext {
	case ".go":
		return "gofmt", []string{"-w", relative}
	case ".rs":
		return "rustfmt", []string{relative}
	case ".dart":
		return "dart", []string{"format", relative}
	case ".py":
		return "ruff", []string{"format", relative}
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp":
		return "clang-format", []string{"-i", relative}
	case ".js", ".jsx", ".ts", ".tsx", ".json", ".css", ".scss", ".md", ".yaml", ".yml":
		prettier := filepath.Join(root, "node_modules", ".bin", executableName("prettier"))
		if isExecutableFile(prettier) {
			return prettier, []string{"--write", relative}
		}
		biome := filepath.Join(root, "node_modules", ".bin", executableName("biome"))
		if isExecutableFile(biome) {
			return biome, []string{"format", "--write", relative}
		}
	}
	return "", nil
}

func (e *Engine) taskFormatFiles(ctx context.Context, args map[string]any, opts HandleOptions, paths []string) (map[string]any, error) {
	formatted, skipped := []string{}, []map[string]any{}
	provider := taskexecution.ProviderLocalWorktree
	for _, path := range paths {
		target, err := e.taskExecutionTargetForPath(ctx, args, opts, path, true)
		if err != nil {
			return nil, err
		}
		if target.Active {
			provider = target.Provider
		}
		if _, err := target.FS.Existing(target.RepositoryPath); err != nil {
			return nil, err
		}
		command, commandArgs := taskFormatter(target.FS.Root, target.RepositoryPath)
		if command == "" {
			skipped = append(skipped, map[string]any{"path": path, "reason": "no formatter available"})
			continue
		}
		cmd := exec.CommandContext(ctx, command, commandArgs...)
		cmd.Dir = target.FS.Root
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("format %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
		formatted = append(formatted, filepath.ToSlash(filepath.Clean(path)))
	}
	return map[string]any{"formatted": formatted, "skipped": skipped, "taskExecution": map[string]any{"taskId": asString(args[privateTaskExecutionID]), "provider": string(provider)}}, nil
}

func (e *Engine) taskPreflight(args map[string]any, opts HandleOptions) (map[string]any, error) {
	command := asString(args["command"])
	if !e.ShellEnabled {
		return map[string]any{"status": "blocked", "riskLevel": "BLOCKED", "reason": "Shell execution is disabled for this workspace.", "matchedRules": []string{"shell-disabled"}, "command": command, "approvalPolicy": "blocked"}, nil
	}
	logicalCWD := strings.TrimSpace(asString(args["cwd"]))
	if logicalCWD == "" {
		logicalCWD = "."
	}
	target, executionCWD, err := e.taskExecutionBindingForCWD(context.Background(), args, opts, logicalCWD, false)
	if err != nil {
		return nil, err
	}
	securityRoot := e.Root
	if target.Active && target.FS != nil {
		securityRoot = target.FS.Root
	}
	result, err := e.preflightAt(command, securityRoot, executionCWD, logicalCWD, opts.SessionID)
	if err == nil {
		if metadata := taskExecutionMetadata(target); metadata != nil {
			result["taskExecution"] = metadata
		}
	}
	return result, err
}

func (e *Engine) taskExecutionBindingForCWD(ctx context.Context, args map[string]any, opts HandleOptions, cwd string, prepare bool) (taskExecutionTarget, string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = "."
	}
	taskID, ownerID := taskExecutionIdentity(args, opts)
	if taskID == "" || e.TaskExecutions == nil {
		absolute, err := e.cwd(cwd)
		return taskExecutionTarget{}, absolute, err
	}
	if cwd == "." && e.TaskExecutionProvider() == taskexecution.ProviderActiveCheckout {
		absolute, err := e.cwd(cwd)
		return taskExecutionTarget{
			TaskID: taskID, OwnerID: ownerID, Provider: taskexecution.ProviderActiveCheckout,
			FS: e.FS, RepositoryPath: ".",
		}, absolute, err
	}
	if cwd != "." {
		target, err := e.taskExecutionTargetForPath(ctx, args, opts, cwd, prepare)
		if err != nil {
			return taskExecutionTarget{}, "", err
		}
		if target.Active {
			return target, target.FS.Root, nil
		}
		absolute, err := e.cwd(cwd)
		return target, absolute, err
	}
	bundle, ok, err := e.TaskExecutions.Store.Get(e.WorkspaceKey, taskID)
	if err != nil {
		return taskExecutionTarget{}, "", err
	}
	if ok && len(bundle.RepositoryBindings) == 1 {
		binding := bundle.RepositoryBindings[0]
		for _, repo := range e.Repositories.All() {
			if repo.ID == binding.RepositoryID && repo.RelativePath == binding.RepositoryPath {
				fs, err := localfs.New(binding.LocalPath)
				if err != nil {
					return taskExecutionTarget{}, "", err
				}
				target := taskExecutionTarget{TaskID: taskID, OwnerID: ownerID, Repository: repo, Binding: binding, Provider: bundle.Provider, FS: fs, RepositoryPath: ".", Active: true}
				return target, fs.Root, nil
			}
		}
	}
	if len(e.Repositories.All()) == 1 {
		repo := e.Repositories.All()[0]
		target, err := e.taskExecutionTargetForPath(ctx, args, opts, repo.RelativePath, prepare)
		if err != nil {
			return taskExecutionTarget{}, "", err
		}
		if target.Active {
			return target, target.FS.Root, nil
		}
	}
	if prepare {
		return taskExecutionTarget{}, "", fmt.Errorf("task-isolated terminal execution in a multi-repository workspace requires cwd to identify a repository")
	}
	absolute, err := e.cwd(cwd)
	return taskExecutionTarget{}, absolute, err
}
