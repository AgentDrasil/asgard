package bwrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/agentwrapper/opencode"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// queryConfigPath is where the query sandbox mounts the aw config. It lives
// under the writable /session bind because /etc is read-only, so bwrap could
// not create a mount point there.
const (
	queryConfigPath         = "/session/aw-config.yaml"
	queryOpencodeConfigPath = "/session/opencode.aw.yaml"
)

// appendQueryConfigArgs mounts the resolved aw config and opencode.aw.yaml
// read-only at fixed paths inside the query sandbox and points aw at them, so
// it does not depend on how (or whether) the host path, which may be a chain
// of symlinks, resolves inside the sandbox.
func appendQueryConfigArgs(args []string) []string {
	awConfig, err := awconfig.Path()
	if err != nil {
		return args
	}
	var resolvedDir string
	if resolved, err := filepath.EvalSymlinks(awConfig); err == nil {
		if fi, err := os.Stat(resolved); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", resolved, queryConfigPath, "--setenv", awconfig.EnvPath, queryConfigPath)
		}
		resolvedDir = filepath.Dir(resolved)
	}
	opencodeCfg := opencode.ConfigPath()
	if opencodeCfg == "" || func() bool { fi, err := os.Stat(opencodeCfg); return err != nil || fi.IsDir() }() {
		if resolvedDir != "" {
			candidate := filepath.Join(resolvedDir, "opencode.aw.yaml")
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				opencodeCfg = candidate
			}
		}
	}
	if opencodeCfg != "" {
		if resolvedOpencode, err := filepath.EvalSymlinks(opencodeCfg); err == nil {
			if fi, err := os.Stat(resolvedOpencode); err == nil && !fi.IsDir() {
				args = append(args, "--ro-bind", resolvedOpencode, queryOpencodeConfigPath, "--setenv", opencode.EnvOpencodeAWConfig, queryOpencodeConfigPath)
			}
		}
	}
	return args
}

// CommandForQuery creates an exec.Cmd that runs `aw <args...>` inside a
// dedicated, ephemeral sandbox. It is used for read-only metadata queries
// (models, usage) that invoke the vendor CLIs non-interactively, which would
// otherwise run on the host and could trigger the CLIs' hooks. The sandbox
// mounts the credentials of every registered CLI, since one `aw` call queries
// them all. The returned cleanup removes the query's throwaway directories.
func CommandForQuery(ctx context.Context, args ...string) (*exec.Cmd, func(), error) {
	return CommandForQueryWithAccounts(ctx, nil, args...)
}

// CommandForQueryWithAccounts is CommandForQuery with accounts pinned
// (cli -> alias): the sandbox binds exactly those accounts and skips account
// selection. With no pins it behaves like CommandForQuery. The query sandbox
// never relays sessions.
func CommandForQueryWithAccounts(ctx context.Context, accts map[string]string, args ...string) (*exec.Cmd, func(), error) {
	chatID := fmt.Sprintf("aw-query-%d-%d", os.Getpid(), time.Now().UnixNano())
	cleanup := func() {
		_ = os.RemoveAll(runnerpaths.SessionDir(chatID))
		_ = os.RemoveAll(runnerpaths.SessionTmpDir(chatID))
	}
	runDir, err := setupSessionDir(chatID)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	spec := SandboxSpec{
		AllCLIs: true,
		Pins:    accts,
		RunDir:  runDir,
		ChatID:  chatID,
	}
	bwrapArgs, err := spec.agentArgs()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	bwrapArgs = append(bwrapArgs, "aw")
	bwrapArgs = append(bwrapArgs, args...)
	cmd, err := newBwrapCmd(ctx, bwrapArgs)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	// The cleanup also releases the --args file, for callers that start the
	// command themselves.
	return cmd, func() { CloseArgs(cmd); cleanup() }, nil
}

// RunQuery runs `aw <args...>` in a query sandbox and returns its stdout. It
// satisfies agentwrapper.QueryRunner.
func RunQuery(ctx context.Context, args ...string) ([]byte, error) {
	return RunQueryWithAccounts(ctx, nil, args...)
}

// RunQueryWithAccounts is RunQuery with accounts pinned (cli -> alias); see
// CommandForQueryWithAccounts.
func RunQueryWithAccounts(ctx context.Context, accts map[string]string, args ...string) ([]byte, error) {
	cmd, cleanup, err := CommandForQueryWithAccounts(ctx, accts, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = StartCmd(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	out := stdout.Bytes()
	if err != nil {
		return nil, fmt.Errorf("running sandboxed aw %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
