package fakebash

import (
	"bytes"
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

	"github.com/creack/pty"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
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

func IsDebugEnabled() bool {
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

func SetupLogger(appName string) {
	home, err := os.UserHomeDir()
	var logDest io.Writer = os.Stderr
	if err == nil {
		logDir := filepath.Join(home, "logs")
		_ = os.MkdirAll(logDir, 0755)
		logFile, err := os.OpenFile(filepath.Join(logDir, appName+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logDest = logFile
		}
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: logDest, TimeFormat: time.RFC3339})

	if IsDebugEnabled() {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Warn().Msgf("Debug mode is enabled in %s", appName)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
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

func FindSocketFD() (int, error) {
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

func RunClient(args []string) error {
	isStatusline := false
	for _, arg := range args {
		if strings.Contains(arg, "agystatusline") {
			isStatusline = true
			break
		}
	}

	if isStatusline {
		var cmdArgs []string
		if len(args) >= 3 && args[1] == "-c" {
			fields := strings.Fields(args[2])
			if len(fields) > 0 {
				cmdArgs = fields
			}
		} else {
			cmdArgs = args[1:]
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

	fd, err := FindSocketFD()
	if err != nil {
		return fmt.Errorf("fakebash error: %w", err)
	}

	file := os.NewFile(uintptr(fd), "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		return fmt.Errorf("fakebash FileConn error: %w", err)
	}
	defer func() { _ = conn.Close() }()

	cwd, _ := os.Getwd()
	env := os.Environ()

	req := Request{
		Args: args[1:],
		Cwd:  cwd,
		Env:  env,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("fakebash marshal error: %w", err)
	}

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, uint32(len(reqBytes)))
	if _, err := conn.Write(lengthBuf); err != nil {
		return fmt.Errorf("fakebash write length error: %w", err)
	}
	if _, err := conn.Write(reqBytes); err != nil {
		return fmt.Errorf("fakebash write request error: %w", err)
	}

	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			return fmt.Errorf("fakebash read frame header error: %w", err)
		}
		msgType := header[0]
		length := binary.BigEndian.Uint32(header[1:5])
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				return fmt.Errorf("fakebash read frame payload error: %w", err)
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

func writeFrame(w io.Writer, msgType byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = msgType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func RunDaemon() error {
	file := os.NewFile(3, "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		return fmt.Errorf("fakebashd FileConn error: %w", err)
	}
	defer func() { _ = conn.Close() }()

	shellCmd := exec.Command("bash")
	shellCmd.Env = os.Environ()

	ptyFile, err := pty.Start(shellCmd)
	if err != nil {
		return fmt.Errorf("fakebashd failed to start shell in PTY: %w", err)
	}
	defer func() { _ = ptyFile.Close() }()

	lengthBuf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, lengthBuf); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("fakebashd read length error: %w", err)
		}
		length := binary.BigEndian.Uint32(lengthBuf)
		reqBytes := make([]byte, length)
		if _, err := io.ReadFull(conn, reqBytes); err != nil {
			return fmt.Errorf("fakebashd read request error: %w", err)
		}

		var req Request
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			log.Error().Err(err).Msg("fakebashd unmarshal error")
			continue
		}

		var cmdStr string
		if len(req.Args) > 0 {
			if req.Args[0] == "-c" {
				if len(req.Args) > 1 {
					cmdStr = strings.Join(req.Args[1:], " ")
				}
			} else {
				cmdStr = strings.Join(req.Args, " ")
			}
		}

		log.Debug().Str("command", cmdStr).Interface("args", req.Args).Msg("fakebashd: command requested")

		if cmdStr == "" {
			if err := writeFrame(conn, TypeExit, []byte("0")); err != nil {
				log.Error().Err(err).Msg("fakebashd write exit frame error")
			}
			continue
		}

		token := uuid.NewString()
		sentinel := "FAKEBASH_DONE_" + token

		var envExports []string
		for _, e := range req.Env {
			if !strings.HasPrefix(e, "_=") && !strings.HasPrefix(e, "SHLVL=") {
				envExports = append(envExports, "export "+e)
			}
		}

		compositeCmd := fmt.Sprintf("(cd %s && %s && %s); echo; echo \"%s:$?\"\n",
			strconv.Quote(req.Cwd),
			strings.Join(envExports, " && "),
			cmdStr,
			sentinel,
		)

		if _, err := ptyFile.Write([]byte(compositeCmd)); err != nil {
			log.Error().Err(err).Msg("fakebashd failed to write to PTY")
			if err := writeFrame(conn, TypeExit, []byte("1")); err != nil {
				log.Error().Err(err).Msg("fakebashd write exit frame error")
			}
			continue
		}

		sentinelBytes := []byte(sentinel)
		buf := make([]byte, 4096)
		var pending []byte
		exitCode := 0

		for {
			n, err := ptyFile.Read(buf)
			if err != nil {
				break
			}
			data := append(pending, buf[:n]...)
			idx := bytes.Index(data, sentinelBytes)
			if idx != -1 {
				before := data[:idx]
				before = bytes.TrimRight(before, "\r\n")
				if len(before) > 0 {
					if err := writeFrame(conn, TypeStdout, before); err != nil {
						log.Error().Err(err).Msg("fakebashd write stdout frame error")
					}
				}

				after := data[idx+len(sentinelBytes):]
				afterStr := string(after)
				parts := strings.Split(afterStr, "\n")
				codeStr := strings.TrimSpace(strings.TrimPrefix(parts[0], ":"))
				if code, err := strconv.Atoi(codeStr); err == nil {
					exitCode = code
				}
				break
			} else {
				keep := len(sentinelBytes)
				if len(data) > keep {
					if err := writeFrame(conn, TypeStdout, data[:len(data)-keep]); err != nil {
						log.Error().Err(err).Msg("fakebashd write stdout frame error")
					}
					pending = data[len(data)-keep:]
				} else {
					pending = data
				}
			}
		}

		if err := writeFrame(conn, TypeExit, []byte(strconv.Itoa(exitCode))); err != nil {
			log.Error().Err(err).Msg("fakebashd write exit frame error")
		}
	}
	return nil
}
