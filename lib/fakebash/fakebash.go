package fakebash

import (
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

func RunClient(args []string) error {
	if len(args) > 1 {
		var cmdArgs []string
		var ok bool
		if strings.HasPrefix(args[1], "-") {
			cmdArgs, ok = unpackCommand(append([]string{"bash"}, args[1:]...))
		} else {
			cmdArgs, ok = unpackCommand(args[1:])
		}

		if ok {
			if len(cmdArgs) == 0 {
				os.Exit(0)
			}

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
	mu sync.Mutex
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

	cmd := exec.CommandContext(stream.Context(), "bash", "-c", cmdStr)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	} else {
		cmd.Env = os.Environ()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Stream stdout
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&pb.CommandResponse{
					Type:    pb.CommandResponse_STDOUT,
					Payload: buf[:n],
				}); sendErr != nil {
					log.Error().Err(sendErr).Msg("fakebashd write stdout frame error")
					return
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Stream stderr
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&pb.CommandResponse{
					Type:    pb.CommandResponse_STDERR,
					Payload: buf[:n],
				}); sendErr != nil {
					log.Error().Err(sendErr).Msg("fakebashd write stderr frame error")
					return
				}
			}
			if err != nil {
				break
			}
		}
	}()

	wg.Wait()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
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
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	if err := grpcServer.Serve(listener); err != nil {
		return fmt.Errorf("grpc server error: %w", err)
	}
	return nil
}

func splitCommandString(cmdStr string) ([][]string, error) {
	var commands [][]string
	var currentCmd []string
	var currentWord strings.Builder

	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	wordStarted := false

	emitWord := func() {
		if wordStarted {
			currentCmd = append(currentCmd, currentWord.String())
			currentWord.Reset()
			wordStarted = false
		}
	}

	emitCommand := func() {
		emitWord()
		if len(currentCmd) > 0 {
			commands = append(commands, currentCmd)
			currentCmd = nil
		}
	}

	for i := 0; i < len(cmdStr); i++ {
		c := cmdStr[i]

		if escaped {
			currentWord.WriteByte(c)
			wordStarted = true
			escaped = false
			continue
		}

		if inSingleQuote {
			if c == '\'' {
				inSingleQuote = false
			} else {
				currentWord.WriteByte(c)
				wordStarted = true
			}
			continue
		}

		if inDoubleQuote {
			switch c {
			case '\\':
				escaped = true
			case '"':
				inDoubleQuote = false
			default:
				currentWord.WriteByte(c)
				wordStarted = true
			}
			continue
		}

		// Not in quotes
		switch c {
		case '\\':
			escaped = true
		case '\'':
			inSingleQuote = true
			wordStarted = true
		case '"':
			inDoubleQuote = true
			wordStarted = true
		case ';', '\n':
			emitCommand()
		case '&', '|':
			emitCommand()
			if i+1 < len(cmdStr) && cmdStr[i+1] == c {
				i++
			}
		case ' ', '\t', '\r':
			emitWord()
		default:
			currentWord.WriteByte(c)
			wordStarted = true
		}
	}

	emitCommand()

	return commands, nil
}

func isNoOpCommand(name string) bool {
	base := filepath.Base(name)
	switch base {
	case "shopt", "true", "false", "colon", ":":
		return true
	}
	return false
}

func unpackCommand(cmd []string) ([]string, bool) {
	if len(cmd) == 0 {
		return nil, true
	}

	first := cmd[0]
	base := filepath.Base(first)

	if base == "exec" {
		return unpackCommand(cmd[1:])
	}

	if isNoOpCommand(first) {
		return nil, true
	}

	if base == "bash" || base == "sh" {
		cIdx := -1
		for i := 1; i < len(cmd); i++ {
			arg := cmd[i]
			if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
				if strings.Contains(arg, "c") {
					cIdx = i
					break
				}
			}
		}

		if cIdx != -1 {
			var innerCmdStr string
			for i := cIdx + 1; i < len(cmd); i++ {
				arg := cmd[i]
				if strings.HasPrefix(arg, "-") {
					continue
				}
				innerCmdStr = arg
				break
			}

			if innerCmdStr != "" {
				innerCmds, err := splitCommandString(innerCmdStr)
				if err != nil {
					return nil, false
				}

				var finalCmd []string
				for _, innerCmd := range innerCmds {
					unpacked, ok := unpackCommand(innerCmd)
					if !ok {
						return nil, false
					}
					if len(unpacked) > 0 {
						if len(finalCmd) > 0 {
							return nil, false
						}
						finalCmd = unpacked
					}
				}
				return finalCmd, true
			}
		}

		return nil, false
	}

	if _, ok := allowlist[first]; ok {
		return cmd, true
	}
	if _, ok := allowlist[base]; ok {
		return cmd, true
	}

	return nil, false
}
