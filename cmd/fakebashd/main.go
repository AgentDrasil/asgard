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
	"strconv"
	"strings"

	"github.com/creack/pty"
	"github.com/google/uuid"
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

func main() {
	file := os.NewFile(3, "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakebashd FileConn error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	shellCmd := exec.Command("bash")
	shellCmd.Env = os.Environ()

	ptyFile, err := pty.Start(shellCmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakebashd failed to start shell in PTY: %v\n", err)
		os.Exit(1)
	}
	defer ptyFile.Close()

	lengthBuf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, lengthBuf); err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "fakebashd read length error: %v\n", err)
			break
		}
		length := binary.BigEndian.Uint32(lengthBuf)
		reqBytes := make([]byte, length)
		if _, err := io.ReadFull(conn, reqBytes); err != nil {
			fmt.Fprintf(os.Stderr, "fakebashd read request error: %v\n", err)
			break
		}

		var req Request
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			fmt.Fprintf(os.Stderr, "fakebashd unmarshal error: %v\n", err)
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

		if cmdStr == "" {
			writeFrame(conn, TypeExit, []byte("0"))
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
			fmt.Fprintf(os.Stderr, "fakebashd failed to write to PTY: %v\n", err)
			writeFrame(conn, TypeExit, []byte("1"))
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
					writeFrame(conn, TypeStdout, before)
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
					writeFrame(conn, TypeStdout, data[:len(data)-keep])
					pending = data[len(data)-keep:]
				} else {
					pending = data
				}
			}
		}

		writeFrame(conn, TypeExit, []byte(strconv.Itoa(exitCode)))
	}
}
