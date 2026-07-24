package fakebash

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/lib/fakebash/pb"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

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
