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
func SetupSSHAgent() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get home directory for ssh-agent setup")
		return
	}

	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); os.IsNotExist(err) {
		log.Debug().Msg("No ~/.ssh directory found, skipping ssh-agent setup")
		return
	}

	// Check if SSH_AUTH_SOCK is already set and working
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if _, err := os.Stat(sock); err == nil {
			log.Info().Str("socket", sock).Msg("Using existing SSH_AUTH_SOCK")
			return
		}
	}

	// Start a background ssh-agent
	out, err := exec.Command("ssh-agent", "-s").Output()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to start ssh-agent")
		return
	}

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
		log.Warn().Msg("ssh-agent output did not yield SSH_AUTH_SOCK")
		return
	}

	log.Info().Str("socket", sock).Msg("Started ssh-agent successfully")

	// Find and add private keys in ~/.ssh
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read ~/.ssh directory")
		return
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
}
