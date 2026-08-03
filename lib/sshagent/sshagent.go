package sshagent

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// SetupSSHAgent starts ssh-agent if not already running, scans ~/.ssh for private keys, and adds them.
func SetupSSHAgent() error {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get user home directory")
		return err
	}

	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); os.IsNotExist(err) {
		if err := os.MkdirAll(sshDir, 0700); err != nil {
			log.Error().Err(err).Str("dir", sshDir).Msg("Failed to create ~/.ssh directory")
			return err
		}
	}

	// Set global GIT_SSH_COMMAND default according to ~/.ssh/config existence
	sshConfigPath := filepath.Join(sshDir, "config")
	if _, err := os.Stat(sshConfigPath); err == nil {
		_ = os.Setenv("GIT_SSH_COMMAND", "ssh -F "+sshConfigPath+" -o StrictHostKeyChecking=no -o IdentitiesOnly=yes")
	} else {
		_ = os.Setenv("GIT_SSH_COMMAND", "ssh -F /dev/null -o StrictHostKeyChecking=no")
	}

	// Check if SSH_AUTH_SOCK is already set and working
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if _, err := os.Stat(sock); err == nil {
			log.Info().Str("socket", sock).Msg("Using existing SSH_AUTH_SOCK")
			return nil
		}
	}

	// Find ssh-agent binary path
	agentBin, err := exec.LookPath("ssh-agent")
	if err != nil {
		log.Warn().Err(err).Msg("ssh-agent binary not found in PATH, skipping agent setup")
		return nil
	}

	// Force socket path to /tmp/asgard-ssh-agent.sock to avoid Read-only file system errors on ~/.ssh
	socketPath := "/tmp/asgard-ssh-agent.sock"
	_ = os.Remove(socketPath)

	// Start a background ssh-agent with explicit socket path in /tmp
	cmd := exec.Command(agentBin, "-a", socketPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		log.Warn().Err(err).Str("stderr", stderr.String()).Msg("ssh-agent failed to execute, skipping agent setup")
		return nil
	}

	_ = os.Setenv("SSH_AUTH_SOCK", socketPath)

	// Parse environment variables output by `ssh-agent -s`
	// Output format:
	// SSH_AUTH_SOCK=/tmp/ssh-XXXXXX/agent.XXXX; export SSH_AUTH_SOCK;
	// SSH_AGENT_PID=1234; export SSH_AGENT_PID;
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ";", 2)
		if len(parts) > 0 {
			kv := strings.SplitN(parts[0], "=", 2)
			if len(kv) == 2 {
				key := strings.TrimSpace(kv[0])
				val := strings.TrimSpace(kv[1])
				if key == "SSH_AUTH_SOCK" || key == "SSH_AGENT_PID" {
					_ = os.Setenv(key, val)
				}
			}
		}
	}

	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return os.ErrNotExist
	}

	// Find and add private keys in ~/.ssh
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip public keys, known_hosts, config, etc.
		if strings.HasSuffix(name, ".pub") || name == "known_hosts" || name == "config" || name == "authorized_keys" {
			continue
		}

		keyPath := filepath.Join(sshDir, name)
		// Try adding the key
		cmd := exec.Command("ssh-add", keyPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			log.Debug().Str("key", name).Err(err).Str("stderr", stderr.String()).Msg("Could not add key to ssh-agent")
		} else {
			log.Info().Str("key", name).Msg("Added SSH key to ssh-agent")
		}
	}

	return nil
}
