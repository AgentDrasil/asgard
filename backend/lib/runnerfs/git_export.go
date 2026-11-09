package runnerfs

// HardenedGitArgs returns the global git arguments every runner-side git
// command must carry: hooks, fsmonitor, pager and local/file transports are
// switched off on the command line, which outranks the repository config.
func HardenedGitArgs() []string { return append([]string(nil), gitSafeArgs...) }

// HardenedGitEnv returns the process environment minus the variables that
// redirect git to another repository or config, with prompts disabled.
func HardenedGitEnv() []string { return gitEnv() }

// DangerousGitConfigKey reports whether a lowercased repository-local config
// key lets the repository run a program, read other config or redirect the
// work tree.
func DangerousGitConfigKey(k string) bool { return dangerousGitKey(k) }
