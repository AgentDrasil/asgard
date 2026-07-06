package main

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
	home, err := os.UserHomeDir()
	var logDest io.Writer = os.Stderr
	if err == nil {
		logDir := filepath.Join(home, "logs")
		_ = os.MkdirAll(logDir, 0755)
		logFile, err := os.OpenFile(filepath.Join(logDir, "fakebashd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logDest = logFile
		}
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: logDest, TimeFormat: time.RFC3339})

	if isDebugEnabled() {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Warn().Msg("Debug mode is enabled in fakebashd")
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

func main() {
	setupLogger()
	log.Info().Msg("fakebashd: started main")
	file := os.NewFile(3, "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		log.Error().Err(err).Msg("fakebashd FileConn error")
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	shellCmd := exec.Command("bash")
	shellCmd.Env = os.Environ()

	ptyFile, err := pty.Start(shellCmd)
	if err != nil {
		log.Error().Err(err).Msg("fakebashd failed to start shell in PTY")
		os.Exit(1)
	}
	defer func() { _ = ptyFile.Close() }()

	lengthBuf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, lengthBuf); err != nil {
			if err == io.EOF {
				break
			}
			log.Error().Err(err).Msg("fakebashd read length error")
			break
		}
		length := binary.BigEndian.Uint32(lengthBuf)
		reqBytes := make([]byte, length)
		if _, err := io.ReadFull(conn, reqBytes); err != nil {
			log.Error().Err(err).Msg("fakebashd read request error")
			break
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
}
