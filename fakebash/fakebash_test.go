package fakebash

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/fakebash/pb"
)

func TestFakebashGRPC_Integration(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fakebash_test.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	tests := []struct {
		name         string
		args         []string
		cwd          string
		env          []string
		wantStdout   string
		wantStderr   string
		wantExitCode string
	}{
		{
			name:         "clean stdout without pty echo",
			args:         []string{"-c", "echo hello-world-test"},
			cwd:          tmpDir,
			wantStdout:   "hello-world-test\n",
			wantStderr:   "",
			wantExitCode: "0",
		},
		{
			name:         "separated stderr stream",
			args:         []string{"-c", "echo err-output >&2"},
			cwd:          tmpDir,
			wantStdout:   "",
			wantStderr:   "err-output\n",
			wantExitCode: "0",
		},
		{
			name:         "environment variables and pwd propagation",
			args:         []string{"-c", "pwd && echo $TEST_VAR"},
			cwd:          tmpDir,
			env:          append(os.Environ(), "TEST_VAR=my_secret_val"),
			wantStdout:   tmpDir + "\nmy_secret_val\n",
			wantStderr:   "",
			wantExitCode: "0",
		},
		{
			name:         "non-zero exit code",
			args:         []string{"-c", "exit 42"},
			cwd:          tmpDir,
			wantStdout:   "",
			wantStderr:   "",
			wantExitCode: "42",
		},
		{
			name:         "positional args preservation",
			args:         []string{"-c", "echo $0 $1", "script_name", "first_arg"},
			cwd:          tmpDir,
			wantStdout:   "script_name first_arg\n",
			wantStderr:   "",
			wantExitCode: "0",
		},
		{
			name:         "command exits while background process holds pipe open",
			args:         []string{"-c", "sleep 10 & echo done"},
			cwd:          tmpDir,
			wantStdout:   "done\n",
			wantStderr:   "",
			wantExitCode: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			stream, err := client.RunCommand(ctx, &pb.CommandRequest{
				Args: tt.args,
				Cwd:  tt.cwd,
				Env:  tt.env,
			})
			require.NoError(t, err)

			var stdoutBuf strings.Builder
			var stderrBuf strings.Builder
			var exitCode string

			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				require.NoError(t, err)

				switch resp.Type {
				case pb.CommandResponse_STDOUT:
					stdoutBuf.Write(resp.Payload)
				case pb.CommandResponse_STDERR:
					stderrBuf.Write(resp.Payload)
				case pb.CommandResponse_EXIT:
					exitCode = string(resp.Payload)
				}
			}

			assert.Equal(t, tt.wantStdout, stdoutBuf.String())
			assert.Equal(t, tt.wantStderr, stderrBuf.String())
			assert.Equal(t, tt.wantExitCode, exitCode)
		})
	}
}

func TestUnpackCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantCmd []string
		wantOk  bool
	}{
		{
			name:    "direct call simple name",
			args:    []string{"agystatusline", "hello"},
			wantCmd: []string{"agystatusline", "hello"},
			wantOk:  true,
		},
		{
			name:    "direct call path",
			args:    []string{"/bin/agystatusline", "hello", "world"},
			wantCmd: []string{"/bin/agystatusline", "hello", "world"},
			wantOk:  true,
		},
		{
			name:    "direct call show-output",
			args:    []string{"show-output"},
			wantCmd: []string{"show-output"},
			wantOk:  true,
		},
		{
			name:    "direct call show-output with arg",
			args:    []string{"show-output", "c-123"},
			wantCmd: []string{"show-output", "c-123"},
			wantOk:  true,
		},
		{
			name:    "bash -c with show-output and flags",
			args:    []string{"-c", "/bin/show-output --tail=50"},
			wantCmd: []string{"/bin/show-output", "--tail=50"},
			wantOk:  true,
		},
		{
			name:    "direct call not allowlisted",
			args:    []string{"ls", "-la"},
			wantCmd: nil,
			wantOk:  false,
		},
		{
			name:    "bash -c with simple allowlisted",
			args:    []string{"-c", "/bin/agystatusline hello"},
			wantCmd: []string{"/bin/agystatusline", "hello"},
			wantOk:  true,
		},
		{
			name:    "bash -c -l wrapper with allowlisted",
			args:    []string{"-c", "-l", "/bin/agystatusline 'hello world'"},
			wantCmd: []string{"/bin/agystatusline", "hello world"},
			wantOk:  true,
		},
		{
			name:    "bash -cl wrapper with allowlisted",
			args:    []string{"-cl", "/bin/agystatusline"},
			wantCmd: []string{"/bin/agystatusline"},
			wantOk:  true,
		},
		{
			name:    "shopt and bash wrapper with allowlisted",
			args:    []string{"-c", "shopt -u promptvars nullglob extglob nocaseglob dotglob; bash -c '/bin/agystatusline \"arg1\" arg2'"},
			wantCmd: []string{"/bin/agystatusline", "arg1", "arg2"},
			wantOk:  true,
		},
		{
			name:    "exec wrapper",
			args:    []string{"-c", "exec /bin/agystatusline"},
			wantCmd: []string{"/bin/agystatusline"},
			wantOk:  true,
		},
		{
			name:    "multiple real commands in sequence - invalid for direct client execution",
			args:    []string{"-c", "/bin/agystatusline hello; /bin/agystatusline world"},
			wantCmd: nil,
			wantOk:  false,
		},
		{
			name:    "shopt only",
			args:    []string{"-c", "shopt -u promptvars"},
			wantCmd: nil,
			wantOk:  true,
		},
		{
			name:    "bypass attempt: non-allowlisted first",
			args:    []string{"-c", "ls find-peer"},
			wantCmd: nil,
			wantOk:  false,
		},
		{
			name:    "bypass attempt: chained command",
			args:    []string{"-c", "find-peer && ls"},
			wantCmd: nil,
			wantOk:  false,
		},
		{
			name:    "bypass attempt: chained command no spaces",
			args:    []string{"-c", "find-peer;ls"},
			wantCmd: nil,
			wantOk:  false,
		},
		{
			name:    "bypass attempt: subshell backticks (treated as literal args)",
			args:    []string{"-c", "find-peer `ls`"},
			wantCmd: []string{"find-peer", "`ls`"},
			wantOk:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotCmd []string
			var gotOk bool
			if len(tt.args) > 0 && strings.HasPrefix(tt.args[0], "-") {
				gotCmd, gotOk = unpackCommand(append([]string{"bash"}, tt.args...))
			} else {
				gotCmd, gotOk = unpackCommand(tt.args)
			}

			assert.Equal(t, tt.wantOk, gotOk)
			assert.Equal(t, tt.wantCmd, gotCmd)
		})
	}
}

func TestFakebash_ProxyEnvDaemonPriority(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fakebash_proxy_env.sock")

	// Set daemon's environment variables
	daemonProxy := "http://127.0.0.1:8082"
	daemonCert := "/etc/ssl/certs/ca-certificates.crt"
	t.Setenv("HTTP_PROXY", daemonProxy)
	t.Setenv("http_proxy", daemonProxy)
	t.Setenv("HTTPS_PROXY", daemonProxy)
	t.Setenv("https_proxy", daemonProxy)
	t.Setenv("ALL_PROXY", daemonProxy)
	t.Setenv("all_proxy", daemonProxy)
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	t.Setenv("no_proxy", "localhost,127.0.0.1")
	t.Setenv("SSL_CERT_FILE", daemonCert)

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// Client maliciously passes evil proxy and altered SSL cert file
	clientEnv := []string{
		"HTTP_PROXY=http://evil.com:9999",
		"http_proxy=http://evil.com:9999",
		"SSL_CERT_FILE=/tmp/malicious.crt",
		"CUSTOM_USER_VAR=user_ok_val",
	}

	stream, err := client.RunCommand(ctx, &pb.CommandRequest{
		Args: []string{"-c", "echo HTTP_PROXY=$HTTP_PROXY && echo http_proxy=$http_proxy && echo SSL_CERT_FILE=$SSL_CERT_FILE && echo CUSTOM_USER_VAR=$CUSTOM_USER_VAR"},
		Cwd:  tmpDir,
		Env:  clientEnv,
	})
	require.NoError(t, err)

	var stdoutBuf strings.Builder
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if resp.Type == pb.CommandResponse_STDOUT {
			stdoutBuf.Write(resp.Payload)
		}
	}

	out := stdoutBuf.String()
	// Malicious client values must be overridden by daemon values
	assert.Contains(t, out, "HTTP_PROXY=http://127.0.0.1:8082")
	assert.Contains(t, out, "http_proxy=http://127.0.0.1:8082")
	assert.Contains(t, out, "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt")
	assert.NotContains(t, out, "http://evil.com")
	assert.NotContains(t, out, "/tmp/malicious.crt")
	// Safe user environment variable must still be preserved
	assert.Contains(t, out, "CUSTOM_USER_VAR=user_ok_val")
}

func TestFakebashGRPC_WatchdogTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fakebash_watchdog.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	pipeline := NewPipeline(storage, nil, nil)

	// Set short watchdog timeout for testing
	t.Setenv("ASGARD_BASH_WATCHDOG_TIMEOUT", "100ms")

	var stdoutBuf, stderrBuf strings.Builder
	// Command outputs a line, sleeps 300ms, outputs another line, and exits
	args := []string{"-c", "echo start && sleep 0.3 && echo after_watchdog"}
	exitCode, err := runStream(context.Background(), client, args, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	out := stdoutBuf.String()
	assert.Contains(t, out, "start\n")
	assert.Contains(t, out, "after_watchdog\n")
	// Verify no duplicated output
	assert.Equal(t, 1, strings.Count(out, "start\n"))
	assert.Equal(t, 1, strings.Count(out, "after_watchdog\n"))
	// Verify no storage header/footer leaked
	assert.NotContains(t, out, "# CMD:")
	assert.NotContains(t, out, "# EXIT:")
}

type errorFakebashServer struct {
	pb.UnimplementedFakebashServiceServer
	mode string
}

func (s *errorFakebashServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_STDOUT,
		Payload: []byte("partial output before termination\n"),
	})

	if s.mode == "recv_error" {
		// Abnormal termination with error
		return io.ErrUnexpectedEOF
	}

	// EOF without EXIT: return nil cleanly without sending EXIT frame
	return nil
}

func TestFakebashGRPC_ErrorPathsFlushed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		serverMode   string
		wantExitCode int
		wantErr      bool
	}{
		{
			name:         "server breaks mid-stream with error (recv error)",
			serverMode:   "recv_error",
			wantExitCode: 1,
			wantErr:      true,
		},
		{
			name:         "server ends stream EOF without EXIT frame",
			serverMode:   "eof_without_exit",
			wantExitCode: 0,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			socketPath := filepath.Join(tmpDir, "error_test.sock")

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })

			grpcServer := grpc.NewServer()
			srv := &errorFakebashServer{mode: tt.serverMode}
			pb.RegisterFakebashServiceServer(grpcServer, srv)

			go func() {
				_ = grpcServer.Serve(listener)
			}()
			t.Cleanup(func() { grpcServer.Stop() })

			grpcConn, err := grpc.NewClient("unix://"+socketPath,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = grpcConn.Close() })

			client := pb.NewFakebashServiceClient(grpcConn)

			storage, err := NewStorage(tmpDir)
			require.NoError(t, err)

			pipeline := NewPipeline(storage, nil, nil)

			var stdoutBuf, stderrBuf strings.Builder
			exitCode, err := runStream(context.Background(), client, []string{"-c", "test"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantExitCode, exitCode)
			// Ensure logged output is flushed to stdout
			assert.Contains(t, stdoutBuf.String(), "partial output before termination\n")
		})
	}
}

func TestFakebashGRPC_ExitCodePreservation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "exit_code_preservation.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	pipeline := NewPipeline(storage, nil, nil)

	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), client, []string{"-c", "exit 42"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)
	require.NoError(t, err)
	assert.Equal(t, 42, exitCode)
}

type hangingErrorServer struct {
	pb.UnimplementedFakebashServiceServer
	hangDuration time.Duration
}

func (s *hangingErrorServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	// 1. Send 1 frame
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_STDOUT,
		Payload: []byte("frame-before-watchdog\n"),
	})

	// 2. Sleep past watchdog timeout
	time.Sleep(s.hangDuration)

	// 3. Fail with error without sending further frames
	return io.ErrUnexpectedEOF
}

func TestFakebashGRPC_WatchdogThenRecvError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "watchdog_error.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &hangingErrorServer{hangDuration: 200 * time.Millisecond}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	pipeline := NewPipeline(storage, nil, nil)

	// Watchdog timeout is 50ms, shorter than server hangDuration 200ms
	t.Setenv("ASGARD_BASH_WATCHDOG_TIMEOUT", "50ms")

	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), client, []string{"-c", "tail -f something"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)

	// Stream returned error, exitCode should be 1
	assert.Error(t, err)
	assert.Equal(t, 1, exitCode)

	// Output generated before watchdog must NEVER be lost
	out := stdoutBuf.String()
	assert.Contains(t, out, "frame-before-watchdog\n")
	assert.NotContains(t, out, "# CMD:")
	assert.NotContains(t, out, "# EXIT:")
}

func TestFakebashGRPC_ExitWithNilPipeline_MemoryFallback(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nil_pipeline_exit.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	var stdoutBuf, stderrBuf strings.Builder
	// p is nil, simulating storage initialization failure
	exitCode, err := runStream(context.Background(), client, []string{"-c", "echo visible-output && echo visible-err >&2"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	assert.Contains(t, stdoutBuf.String(), "visible-output\n")
	assert.Contains(t, stderrBuf.String(), "visible-err\n")
}

type envCaptureServer struct {
	pb.UnimplementedFakebashServiceServer
	capturedEnv []string
	mu          sync.Mutex
}

func (s *envCaptureServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	s.mu.Lock()
	s.capturedEnv = append([]string(nil), req.Env...)
	s.mu.Unlock()

	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_STDOUT,
		Payload: []byte("ok\n"),
	})
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_EXIT,
		Payload: []byte("0"),
	})
	return nil
}

func TestFakebashGRPC_StripSecretEnv(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "strip_secret_env.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &envCaptureServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	testEnv := []string{
		"NORMAL_KEY=some_value",
		"GEMINI_API_KEY=ai-secret-gemini-token",
		"TYPESAFE_API_KEY=ai-secret-typesafe-token",
		"ANOTHER_VAR=12345",
	}

	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), client, []string{"-c", "echo test"}, tmpDir, testEnv, &stdoutBuf, &stderrBuf, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	srv.mu.Lock()
	captured := srv.capturedEnv
	srv.mu.Unlock()

	require.NotEmpty(t, captured)
	for _, envVar := range captured {
		assert.False(t, strings.HasPrefix(envVar, "GEMINI_API_KEY="), "GEMINI_API_KEY must not be forwarded")
		assert.False(t, strings.HasPrefix(envVar, "TYPESAFE_API_KEY="), "TYPESAFE_API_KEY must not be forwarded")
	}
	assert.Contains(t, captured, "NORMAL_KEY=some_value")
	assert.Contains(t, captured, "ANOTHER_VAR=12345")
}

func TestFakebashGRPC_MemoryBufferCap_PipelineCompressed(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "buffer_cap.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	// Pipeline with evaluator returning StrategyDropOnSuccess
	pipeline := NewPipeline(storage, &fakeEvaluator{strategy: StrategyDropOnSuccess}, nil)

	// Produce ~1.5MB output, which exceeds maxMemoryBufferLimit (1MB)
	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), client, []string{"-c", "head -c 1572864 /dev/zero | tr '\\0' 'A'"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	// Output must NOT be bypassed; it should be compressed by pipeline without duplicate chunks.
	outStr := stdoutBuf.String()
	assert.Contains(t, outStr, "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]")
	assert.NotContains(t, outStr, strings.Repeat("A", 1000))
	assert.True(t, len(outStr) < 2048, "Output should be compressed summary, got %d bytes", len(outStr))
}

type capThenWatchdogServer struct {
	pb.UnimplementedFakebashServiceServer
	hangDuration time.Duration
}

func (s *capThenWatchdogServer) RunCommand(req *pb.CommandRequest, stream pb.FakebashService_RunCommandServer) error {
	// 1. Send 1.5MB (> 1MB buffer cap)
	chunk := bytes.Repeat([]byte("A"), 64*1024)
	for i := 0; i < 24; i++ {
		_ = stream.Send(&pb.CommandResponse{
			Type:    pb.CommandResponse_STDOUT,
			Payload: chunk,
		})
	}
	// Tag the pre-watchdog chunk
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_STDOUT,
		Payload: []byte("MID_MARKER\n"),
	})

	// 2. Sleep past watchdog timeout
	time.Sleep(s.hangDuration)

	// 3. Send post-watchdog output
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_STDOUT,
		Payload: []byte("POST_WATCHDOG_MARKER\n"),
	})

	// 4. Send EXIT frame
	_ = stream.Send(&pb.CommandResponse{
		Type:    pb.CommandResponse_EXIT,
		Payload: []byte("0"),
	})
	return nil
}

func TestFakebashGRPC_CapThenWatchdog_NoOutputGap(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "cap_watchdog.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	srv := &capThenWatchdogServer{hangDuration: 150 * time.Millisecond}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	client := pb.NewFakebashServiceClient(grpcConn)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	pipeline := NewPipeline(storage, nil, nil)

	// Set short watchdog duration (50ms) so it fires during server sleep
	t.Setenv("ASGARD_BASH_WATCHDOG_TIMEOUT", "50ms")

	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), client, []string{"-c", "long_job"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	out := stdoutBuf.String()
	// Both pre-cap, mid (post-cap but pre-watchdog), and post-watchdog markers must be present
	assert.Contains(t, out, "MID_MARKER\n", "mid-stream output after buffer cap but before watchdog must not be lost")
	assert.Contains(t, out, "POST_WATCHDOG_MARKER\n", "post-watchdog output must be streamed in realtime")

	// Total length should include all 24 * 64KB + markers
	expectedMinBytes := 24*64*1024 + len("MID_MARKER\n") + len("POST_WATCHDOG_MARKER\n")
	assert.GreaterOrEqual(t, len(out), expectedMinBytes, "output must not lose any chunk")

	// Check that markers only appear once (no duplicate replay)
	assert.Equal(t, 1, strings.Count(out, "MID_MARKER\n"))
	assert.Equal(t, 1, strings.Count(out, "POST_WATCHDOG_MARKER\n"))

	// Ensure no raw storage protocol headers/footers leak into stdout
	assert.NotContains(t, out, "# CMD:")
	assert.NotContains(t, out, "# EXIT:")
}
