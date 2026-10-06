package secscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrUnavailable = errors.New("security scanner unavailable")

type commandResult struct {
	Stdout []byte
	Stderr []byte
}

func runCommand(ctx context.Context, name string, args ...string) (commandResult, error) {
	path, err := scannerExecutable(name)
	if err != nil {
		return commandResult{}, err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, path, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, fmt.Errorf("%s failed: %s", name, redactText(message))
	}
	return commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func scannerVersion(ctx context.Context, name string, args ...string) string {
	result, err := runCommand(ctx, name, args...)
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(result.Stdout))
	if index := strings.IndexByte(version, '\n'); index >= 0 {
		version = version[:index]
	}
	return version
}
