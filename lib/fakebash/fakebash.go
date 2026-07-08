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

// TODO: need a robust parser of command.
func RunClient(args []string) error {
	isAllowlisted := false
	if len(args) > 1 {
		var targetCmd string
		if len(args) >= 3 && args[1] == "-c" {
			fields := strings.Fields(args[2])
			if len(fields) > 0 {
				targetCmd = fields[0]
			}
		} else {
			targetCmd = args[1]
		}

		if targetCmd != "" {
			if _, ok := allowlist[targetCmd]; ok {
				isAllowlisted = true
			} else if _, ok := allowlist[filepath.Base(targetCmd)]; ok {
				isAllowlisted = true
			}
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

	grpcConn, err := grpc.NewClient("unix:///fakebash/fakebash.sock",
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
	socketPath := "/fakebash/fakebash.sock"
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("fakebashd failed to listen on unix socket: %w", err)
	}
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
