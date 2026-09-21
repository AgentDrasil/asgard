package fakebash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/fakebash/pb"
)

var allowlist = map[string]struct{}{
	"agystatusline": {},
	"find-peer":     {},
	"call-peer":     {},
	"ask-user":      {},
	"ask_user":      {},
	"show-output":   {},
}

var ProtectedProxyEnvKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
	"ALL_PROXY", "all_proxy",
	"NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE",
}

// secretEnvDenylist contains sensitive API keys that must not be forwarded from the agent
// environment to the command execution sandbox.
var secretEnvDenylist = []string{
	"GEMINI_API_KEY",
	"TYPESAFE_API_KEY",
}

// stripSecretEnv removes sensitive credential keys from the environment slice.
func stripSecretEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		stripped := false
		for _, secretKey := range secretEnvDenylist {
			if strings.HasPrefix(e, secretKey+"=") {
				stripped = true
				break
			}
		}
		if !stripped {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// maxMemoryBufferLimit defines the upper bound (1MB) for in-memory buffers in non-passthrough mode.
// When output exceeds this limit, fakebash immediately transitions to realtime passthrough to
// maintain O(1) memory overhead.
const maxMemoryBufferLimit = 1024 * 1024

// flushBuffers flushes raw output to stdout and stderr, checking storage first and falling
// back to in-memory buffers if storage or pipeline is unavailable or reading failed.
func flushBuffers(stdout, stderr io.Writer, p *Pipeline, storageFile StorageFile, cmdID string, stdoutBuf, stderrBuf *bytes.Buffer) {
	if storageFile != nil && p != nil && p.storage != nil {
		raw, rErr := p.storage.ReadPayload(cmdID)
		if rErr == nil {
			_, _ = stdout.Write(raw)
			return
		}
	}
	if stdoutBuf.Len() > 0 {
		_, _ = stdout.Write(stdoutBuf.Bytes())
		stdoutBuf.Reset()
	}
	if stderrBuf.Len() > 0 {
		_, _ = stderr.Write(stderrBuf.Bytes())
		stderrBuf.Reset()
	}
}

// runStream executes the gRPC command stream against fakebashd, coordinates
// streaming log persistence, watchdog fallback, and pipeline summarization.
func runStream(ctx context.Context, client pb.FakebashServiceClient, args []string, cwd string, env []string, stdout, stderr io.Writer, p *Pipeline) (int, error) {
	cmdStr := strings.Join(args, " ")
	log.Info().Interface("args", args).Str("cwd", cwd).Msg("fakebash: forwarding command to fakebashd via gRPC")
	startTime := time.Now()

	var storageFile StorageFile
	var cmdID string
	var err error

	if p != nil && p.storage != nil {
		storageFile, err = p.storage.CreateFile(cmdStr)
		if err != nil {
			log.Debug().Err(err).Msg("fakebash: failed to create storage file, continuing without persistence")
		} else {
			cmdID = storageFile.ID()
		}
	}

	stream, err := client.RunCommand(ctx, &pb.CommandRequest{
		Args: args,
		Cwd:  cwd,
		Env:  stripSecretEnv(env),
	})
	if err != nil {
		log.Error().Err(err).Msg("fakebash: RunCommand RPC failed")
		return 1, fmt.Errorf("run command stream error: %w", err)
	}

	// Watchdog duration: default 30s, customizable via ASGARD_BASH_WATCHDOG_TIMEOUT (ms or duration)
	watchdogDuration := 30 * time.Second
	if envTimeout := os.Getenv("ASGARD_BASH_WATCHDOG_TIMEOUT"); envTimeout != "" {
		if d, parseErr := time.ParseDuration(envTimeout); parseErr == nil && d > 0 {
			watchdogDuration = d
		} else if ms, parseErr := strconv.Atoi(envTimeout); parseErr == nil && ms > 0 {
			watchdogDuration = time.Duration(ms) * time.Millisecond
		}
	}

	var timer *time.Timer
	var passthrough atomic.Bool
	var totalBytes int64
	var lineCount int

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	replayed := false

	startWatchdogOnce := sync.Once{}

	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				// Path 3: EOF without EXIT frame
				log.Warn().Dur("elapsed", time.Since(startTime)).Msg("fakebash: stream ended without EXIT frame, flushing raw output")
				if storageFile != nil {
					_ = storageFile.Finish(0)
				}
				if !replayed {
					flushBuffers(stdout, stderr, p, storageFile, cmdID, &stdoutBuf, &stderrBuf)
				}
				return 0, nil
			}

			// Path 2: recv non-EOF error
			log.Error().Err(err).Dur("elapsed", time.Since(startTime)).Msg("fakebash: stream recv error from fakebashd, flushing raw output")
			if storageFile != nil {
				_ = storageFile.Finish(1)
			}
			if !replayed {
				flushBuffers(stdout, stderr, p, storageFile, cmdID, &stdoutBuf, &stderrBuf)
			}
			return 1, fmt.Errorf("stream recv error: %w", err)
		}

		// Start watchdog timer upon receiving the first frame
		startWatchdogOnce.Do(func() {
			timer = time.AfterFunc(watchdogDuration, func() {
				log.Warn().Dur("watchdogDuration", watchdogDuration).Msg("fakebash: watchdog timeout triggered; switching to realtime passthrough")
				passthrough.Store(true)
			})
		})

		isPassthrough := passthrough.Load()
		if isPassthrough && !replayed {
			replayed = true
			flushBuffers(stdout, stderr, p, storageFile, cmdID, &stdoutBuf, &stderrBuf)
		}

		switch resp.Type {
		case pb.CommandResponse_STDOUT:
			totalBytes += int64(len(resp.Payload))
			for _, b := range resp.Payload {
				if b == '\n' {
					lineCount++
				}
			}
			if storageFile != nil {
				_ = storageFile.Append(resp.Payload)
			}
			if isPassthrough {
				_, _ = stdout.Write(resp.Payload)
			} else {
				if stdoutBuf.Len()+stderrBuf.Len()+len(resp.Payload) > maxMemoryBufferLimit {
					log.Info().Msg("fakebash: in-memory buffer exceeded limit; stopping memory buffering to bound memory usage")
				} else {
					stdoutBuf.Write(resp.Payload)
				}
			}
		case pb.CommandResponse_STDERR:
			totalBytes += int64(len(resp.Payload))
			for _, b := range resp.Payload {
				if b == '\n' {
					lineCount++
				}
			}
			if storageFile != nil {
				_ = storageFile.Append(resp.Payload)
			}
			if isPassthrough {
				_, _ = stderr.Write(resp.Payload)
			} else {
				if stdoutBuf.Len()+stderrBuf.Len()+len(resp.Payload) > maxMemoryBufferLimit {
					log.Info().Msg("fakebash: in-memory buffer exceeded limit; stopping memory buffering to bound memory usage")
				} else {
					stderrBuf.Write(resp.Payload)
				}
			}
		case pb.CommandResponse_EXIT:
			// Path 1: Normal EXIT frame
			if timer != nil {
				timer.Stop()
			}
			exitCode := 0
			if len(resp.Payload) > 0 {
				exitCode, _ = strconv.Atoi(string(resp.Payload))
			}
			log.Info().Int("exitCode", exitCode).Dur("elapsed", time.Since(startTime)).Msg("fakebash: received EXIT frame from fakebashd")

			if storageFile != nil {
				_ = storageFile.Finish(exitCode)
			}

			if replayed && isPassthrough {
				// Already streamed raw output in realtime via watchdog; nothing left to replay.
				return exitCode, nil
			}

			if p != nil {
				processed, pErr := p.Process(ctx, cmdStr, cmdID, totalBytes, lineCount, exitCode)
				if pErr == nil {
					_, _ = stdout.Write([]byte(processed))
					if !strings.HasSuffix(processed, "\n") {
						_, _ = stdout.Write([]byte("\n"))
					}
					return exitCode, nil
				}
				log.Warn().Err(pErr).Msg("fakebash: pipeline processing failed; falling back to raw output")
			}

			// Fallback: flush raw output from storage, or memory buffers if storage/pipeline is unavailable
			flushBuffers(stdout, stderr, p, storageFile, cmdID, &stdoutBuf, &stderrBuf)
			return exitCode, nil
		}
	}
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

	storage, err := NewStorage("")
	if err != nil {
		log.Debug().Err(err).Msg("failed to initialize storage; proceeding without pipeline")
	}

	var pipeline *Pipeline
	if storage != nil {
		evaluator := NewJevEvaluator(storage)
		summarizer := NewGenAISummarizer(storage)
		pipeline = NewPipeline(storage, evaluator, summarizer)
	}

	code, err := runStream(context.Background(), client, args[1:], cwd, env, os.Stdout, os.Stderr, pipeline)
	if err != nil {
		log.Error().Err(err).Msg("fakebash stream error")
	}
	os.Exit(code)
	return nil
}

type fakebashServer struct {
	pb.UnimplementedFakebashServiceServer
}

func (s *fakebashServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	var cmd *exec.Cmd
	var cmdStr string
	if len(req.Args) > 0 {
		if req.Args[0] == "-c" {
			if len(req.Args) > 1 {
				cmdStr = req.Args[1]
				cmd = exec.CommandContext(stream.Context(), "bash", append([]string{"-c"}, req.Args[1:]...)...)
			}
		} else {
			cmdStr = strings.Join(req.Args, " ")
			cmd = exec.CommandContext(stream.Context(), req.Args[0], req.Args[1:]...)
		}
	}

	log.Info().Str("command", cmdStr).Interface("args", req.Args).Str("cwd", req.Cwd).Msg("fakebashd: command requested")

	if cmdStr == "" {
		if err := stream.Send(&pb.CommandResponse{
			Type:    pb.CommandResponse_EXIT,
			Payload: []byte("0"),
		}); err != nil {
			log.Error().Err(err).Msg("fakebashd write exit frame error")
		}
		return nil
	}
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if len(req.Env) > 0 {
		cmd.Env = append([]string(nil), req.Env...)
		// Daemon priority override (R4): ProtectedProxyEnvKeys from fakebashd daemon take precedence over req.Env
		daemonEnvMap := make(map[string]string)
		for _, e := range os.Environ() {
			if k, v, ok := strings.Cut(e, "="); ok {
				daemonEnvMap[k] = v
			}
		}

		for _, protectedKey := range ProtectedProxyEnvKeys {
			if daemonVal, ok := daemonEnvMap[protectedKey]; ok {
				// Remove any existing entries of protectedKey from cmd.Env
				filtered := make([]string, 0, len(cmd.Env))
				prefix := protectedKey + "="
				for _, e := range cmd.Env {
					if !strings.HasPrefix(e, prefix) {
						filtered = append(filtered, e)
					}
				}
				cmd.Env = append(filtered, prefix+daemonVal)
			}
		}
	} else {
		cmd.Env = os.Environ()
	}

	cmd.WaitDelay = 200 * time.Millisecond

	// Use pipes we own instead of StdoutPipe/StderrPipe: exec.Cmd.Wait closes
	// those pipes as soon as the process exits, discarding any output the
	// reader goroutines have not consumed yet (WaitDelay does not help because
	// StdoutPipe registers no copy goroutines for awaitGoroutines to wait on).
	stdoutPipe, stdoutWrite, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdoutPipe.Close()
		_ = stdoutWrite.Close()
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite

	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		_ = stdoutPipe.Close()
		_ = stdoutWrite.Close()
		_ = stderrPipe.Close()
		_ = stderrWrite.Close()
		return fmt.Errorf("failed to start command: %w", err)
	}
	// The child holds its own duplicates of the write ends; close the parent
	// copies so EOF is delivered once the child (and any processes holding the
	// inherited fds) exit.
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	log.Info().Str("command", cmdStr).Int("pid", cmd.Process.Pid).Msg("fakebashd: process started")

	var streamMu sync.Mutex
	sendResponse := func(resp *pb.CommandResponse) error {
		streamMu.Lock()
		defer streamMu.Unlock()
		return stream.Send(resp)
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
				if sendErr := sendResponse(&pb.CommandResponse{
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
				if sendErr := sendResponse(&pb.CommandResponse{
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

	var waitErr error
	pipesDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(pipesDone)
	}()

	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- cmd.Wait()
	}()

	select {
	case <-pipesDone:
		// Normal case: pipes closed (EOF reached), now collect process exit status
		waitErr = <-waitErrCh
		log.Info().Str("command", cmdStr).Dur("elapsed", time.Since(startTime)).Msg("fakebashd: pipes drained normally before/at process exit")
	case waitErr = <-waitErrCh:
		// Process exited, but pipes may still be held open by lingering child processes.
		// Wait for pipes to finish draining or force close them after a short delay.
		log.Info().Str("command", cmdStr).Dur("elapsed", time.Since(startTime)).Msg("fakebashd: process exited, waiting up to 100ms for pipes to drain")
		select {
		case <-pipesDone:
		case <-time.After(100 * time.Millisecond):
			_ = stdoutPipe.Close()
			_ = stderrPipe.Close()
			<-pipesDone
			log.Warn().Str("command", cmdStr).Msg("fakebashd: pipes forcibly closed after 100ms grace period")
		}
	}

	// Readers have returned by this point, so closing cannot race with reads.
	_ = stdoutPipe.Close()
	_ = stderrPipe.Close()

	exitCode := 0
	if waitErr != nil {
		if errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			exitCode = 0
		} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	log.Info().Str("command", cmdStr).Int("exitCode", exitCode).Dur("elapsed", time.Since(startTime)).Msg("fakebashd: command finished")

	if err := sendResponse(&pb.CommandResponse{
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

func splitCommandString(cmdStr string) [][]string {
	var commands [][]string
	var currentCmd []string
	var currentWord strings.Builder

	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	wordStarted := false

	emitWord := func() {
		if wordStarted || currentWord.Len() > 0 {
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
			escaped = false
			wordStarted = true
			continue
		}

		if c == '\\' && !inSingleQuote {
			escaped = true
			wordStarted = true
			continue
		}

		if c == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			wordStarted = true
			continue
		}

		if c == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			wordStarted = true
			continue
		}

		if !inSingleQuote && !inDoubleQuote {
			if c == ';' || c == '\n' {
				emitCommand()
				continue
			}
			if c == '&' || c == '|' {
				// Handle && or || operators
				if i+1 < len(cmdStr) && cmdStr[i+1] == c {
					i++
				}
				emitCommand()
				continue
			}
			if unicode.IsSpace(rune(c)) {
				emitWord()
				continue
			}
		}

		currentWord.WriteByte(c)
		wordStarted = true
	}

	emitCommand()

	return commands
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
				innerCmds := splitCommandString(innerCmdStr)

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
				if len(finalCmd) > 0 {
					return finalCmd, true
				}
				return nil, true
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
