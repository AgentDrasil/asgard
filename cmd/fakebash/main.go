package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
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

func main() {
	fd, err := findSocketFD()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakebash error: %v\n", err)
		os.Exit(1)
	}

	file := os.NewFile(uintptr(fd), "socket")
	conn, err := net.FileConn(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakebash FileConn error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	cwd, _ := os.Getwd()
	env := os.Environ()

	req := Request{
		Args: os.Args[1:],
		Cwd:  cwd,
		Env:  env,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakebash marshal error: %v\n", err)
		os.Exit(1)
	}

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, uint32(len(reqBytes)))
	if _, err := conn.Write(lengthBuf); err != nil {
		fmt.Fprintf(os.Stderr, "fakebash write length error: %v\n", err)
		os.Exit(1)
	}
	if _, err := conn.Write(reqBytes); err != nil {
		fmt.Fprintf(os.Stderr, "fakebash write request error: %v\n", err)
		os.Exit(1)
	}

	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			fmt.Fprintf(os.Stderr, "fakebash read frame header error: %v\n", err)
			os.Exit(1)
		}
		msgType := header[0]
		length := binary.BigEndian.Uint32(header[1:5])
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				fmt.Fprintf(os.Stderr, "fakebash read frame payload error: %v\n", err)
				os.Exit(1)
			}
		}

		switch msgType {
		case TypeStdout:
			os.Stdout.Write(payload)
		case TypeStderr:
			os.Stderr.Write(payload)
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
