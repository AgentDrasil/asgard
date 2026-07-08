package fakebash

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/lib/fakebash/pb"
)

var allowlist = map[string]struct{}{
	"agystatusline": {},
	"find-peer":     {},
	"call-peer":     {},
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
	isAllowlisted := false
	for _, arg := range args {
		if _, ok := allowlist[arg]; ok {
			isAllowlisted = true
			break
		}
		if _, ok := allowlist[filepath.Base(arg)]; ok {
			isAllowlisted = true
			break
		}
	}

	if isAllowlisted {
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
				log.Error().Err(err).Msg("fakebash run allowlisted command error")
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

	var once sync.Once
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		var c net.Conn
		once.Do(func() {
			c = conn
		})
		if c == nil {
			return nil, io.EOF
		}
		return c, nil
	}

	grpcConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("grpc dial error: %w", err)
	}
	defer func() { _ = grpcConn.Close() }()

	client := pb.NewFakebashServiceClient(grpcConn)

	cwd, _ := os.Getwd()
	env := os.Environ()

	stream, err := client.RunCommand(context.Background(), &pb.CommandRequest{
		Args: args[1:],
		Cwd:  cwd,
		Env:  env,
	})
	if err != nil {
		return fmt.Errorf("run command stream error: %w", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("stream recv error: %w", err)
		}

		switch resp.Type {
		case pb.CommandResponse_STDOUT:
			_, _ = os.Stdout.Write(resp.Payload)
		case pb.CommandResponse_STDERR:
			_, _ = os.Stderr.Write(resp.Payload)
		case pb.CommandResponse_EXIT:
			if len(resp.Payload) > 0 {
				code, _ := strconv.Atoi(string(resp.Payload))
				os.Exit(code)
			}
			os.Exit(0)
		}
	}
	return nil
}

type SingleConnListener struct {
	conn     net.Conn
	done     chan struct{}
	accepted bool
	closed   bool
	mu       sync.Mutex
}

func NewSingleConnListener(c net.Conn) *SingleConnListener {
	return &SingleConnListener{
		conn: c,
		done: make(chan struct{}),
	}
}

func (l *SingleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, io.EOF
	}
	if l.accepted {
		l.mu.Unlock()
		<-l.done
		return nil, io.EOF
	}
	c := l.conn
	l.conn = nil
	l.accepted = true
	l.mu.Unlock()

	return c, nil
}

func (l *SingleConnListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	if l.conn != nil {
		_ = l.conn.Close()
		l.conn = nil
	}
	close(l.done)
	l.mu.Unlock()
	return nil
}

func (l *SingleConnListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		return l.conn.LocalAddr()
	}
	return &net.UnixAddr{Name: "single-conn", Net: "unix"}
}

type fakebashServer struct {
	pb.UnimplementedFakebashServiceServer
	mu       sync.Mutex
	ptyFile  *os.File
	shellCmd *exec.Cmd
}

func (s *fakebashServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	log.Debug().Str("command", cmdStr).Interface("args", req.Args).Msg("fakebashd gRPC: command requested")

	if cmdStr == "" {
		if err := stream.Send(&pb.CommandResponse{
			Type:    pb.CommandResponse_EXIT,
			Payload: []byte("0"),
		}); err != nil {
			log.Error().Err(err).Msg("fakebashd write exit frame error")
		}
		return nil
	}

	if s.ptyFile == nil {
		s.shellCmd = exec.Command("bash")
		s.shellCmd.Env = os.Environ()

		ptyFile, err := pty.Start(s.shellCmd)
		if err != nil {
			return fmt.Errorf("fakebashd failed to start shell in PTY: %w", err)
		}
		s.ptyFile = ptyFile
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

	if _, err := s.ptyFile.Write([]byte(compositeCmd)); err != nil {
		log.Error().Err(err).Msg("fakebashd failed to write to PTY")
		if err := stream.Send(&pb.CommandResponse{
			Type:    pb.CommandResponse_EXIT,
			Payload: []byte("1"),
		}); err != nil {
			log.Error().Err(err).Msg("fakebashd write exit frame error")
		}
		return nil
	}

	sentinelBytes := []byte(sentinel)
	buf := make([]byte, 4096)
	var pending []byte
	exitCode := 0

	for {
		n, err := s.ptyFile.Read(buf)
		if err != nil {
			// If PTY is closed/broken, reset PTY so next command starts a new shell
			_ = s.ptyFile.Close()
			s.ptyFile = nil
			s.shellCmd = nil
			break
		}
		data := append(pending, buf[:n]...)
		idx := bytes.Index(data, sentinelBytes)
		if idx != -1 {
			before := data[:idx]
			before = bytes.TrimRight(before, "\r\n")
			if len(before) > 0 {
				if err := stream.Send(&pb.CommandResponse{
					Type:    pb.CommandResponse_STDOUT,
					Payload: before,
				}); err != nil {
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
				if err := stream.Send(&pb.CommandResponse{
					Type:    pb.CommandResponse_STDOUT,
					Payload: data[:len(data)-keep],
				}); err != nil {
					log.Error().Err(err).Msg("fakebashd write stdout frame error")
				}
				pending = data[len(data)-keep:]
			} else {
				pending = data
			}
		}
	}

	if err := stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_EXIT,
		Payload: []byte(strconv.Itoa(exitCode)),
	}); err != nil {
		log.Error().Err(err).Msg("fakebashd write exit frame error")
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

	listener := NewSingleConnListener(conn)
	defer func() { _ = listener.Close() }()

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	defer func() {
		if srv.ptyFile != nil {
			_ = srv.ptyFile.Close()
		}
	}()
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	if err := grpcServer.Serve(listener); err != nil {
		return fmt.Errorf("grpc server error: %w", err)
	}
	return nil
}
