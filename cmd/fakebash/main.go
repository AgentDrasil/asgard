package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Request struct {
	Args []string `json:"args"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

const (
	TypeStdout = 1
	TypeStderr = 2
	TypeExit   = 3
)

func isDebugEnabled() bool {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		if _, err := os.Stat("/home/user/config.yaml"); err == nil {
			configPath = "/home/user/config.yaml"
		} else {
			configPath = "config.yaml"
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	var cfg struct {
		Debug bool `yaml:"debug"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.Debug
}

func setupLogger() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	if isDebugEnabled() {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Warn().Msg("Debug mode is enabled in fakebash")
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

func main() {
	setupLogger()
	log.Debug().Interface("args", os.Args).Msg("fakebash: command requested")

	isStatusline := false
	for _, arg := range os.Args {
		if strings.Contains(arg, "agystatusline") {
			isStatusline = true
			break
		}
	}

	if isStatusline {
		var cmdArgs []string
		if len(os.Args) >= 3 && os.Args[1] == "-c" {
			fields := strings.Fields(os.Args[2])
			if len(fields) > 0 {
				cmdArgs = fields
			}
		} else {
			cmdArgs = os.Args[1:]
		}

		if len(cmdArgs) > 0 {
			cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = os.Environ()
			err := cmd.Run()
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					os.Exit(exitErr.ExitCode())
				}
				log.Error().Err(err).Msg("fakebash run statusline error")
				os.Exit(1)
			}
			os.Exit(0)
		}
	}

	fd, err := findSocketFD()
	if err != nil {
		log.Error().Err(err).Msg("fakebash error")
		os.Exit(1)
	}

	file := os.NewFile(uintptr(fd), "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		log.Error().Err(err).Msg("fakebash FileConn error")
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	cwd, _ := os.Getwd()
	env := os.Environ()

	req := Request{
		Args: os.Args[1:],
		Cwd:  cwd,
		Env:  env,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		log.Error().Err(err).Msg("fakebash marshal error")
		os.Exit(1)
	}

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, uint32(len(reqBytes)))
	if _, err := conn.Write(lengthBuf); err != nil {
		log.Error().Err(err).Msg("fakebash write length error")
		os.Exit(1)
	}
	if _, err := conn.Write(reqBytes); err != nil {
		log.Error().Err(err).Msg("fakebash write request error")
		os.Exit(1)
	}

	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			log.Error().Err(err).Msg("fakebash read frame header error")
			os.Exit(1)
		}
		msgType := header[0]
		length := binary.BigEndian.Uint32(header[1:5])
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				log.Error().Err(err).Msg("fakebash read frame payload error")
				os.Exit(1)
			}
		}

		switch msgType {
		case TypeStdout:
			_, _ = os.Stdout.Write(payload)
		case TypeStderr:
			_, _ = os.Stderr.Write(payload)
		case TypeExit:
			if len(payload) > 0 {
				code, _ := strconv.Atoi(string(payload))
				os.Exit(code)
			}
			os.Exit(0)
		}
	}
}

func isFDValid(fd int) bool {
	var stat syscall.Stat_t
	err := syscall.Fstat(fd, &stat)
	return err == nil
}

func isFDValidPath(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func findSocketFD() (int, error) {
	if isFDValid(3) {
		return 3, nil
	}
	if isFDValidPath("/proc/1/fd/3") {
		f, err := os.OpenFile("/proc/1/fd/3", os.O_RDWR, 0)
		if err == nil {
			return int(f.Fd()), nil
		}
	}
	if isFDValidPath("/proc/self/fd/3") {
		f, err := os.OpenFile("/proc/self/fd/3", os.O_RDWR, 0)
		if err == nil {
			return int(f.Fd()), nil
		}
	}
	dirs, err := os.ReadDir("/proc")
	if err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			if d.Name() == "self" {
				continue
			}
			pidPath := filepath.Join("/proc", d.Name(), "fd", "3")
			if isFDValidPath(pidPath) {
				f, err := os.OpenFile(pidPath, os.O_RDWR, 0)
				if err == nil {
					return int(f.Fd()), nil
				}
			}
		}
	}
	return -1, fmt.Errorf("no shared socket fd found")
}
