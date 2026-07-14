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

func TestFakebashGRPC(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fakebash_test.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	defer grpcServer.Stop()

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer func() { _ = grpcConn.Close() }()

	client := pb.NewFakebashServiceClient(grpcConn)

	// Test RunCommand
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	stream, err := client.RunCommand(ctx, &pb.CommandRequest{
		Args: []string{"echo", "hello-world-test"},
		Cwd:  cwd,
		Env:  []string{"FOO=bar"},
	})
	require.NoError(t, err)

	var outputs []string
	var exitCode string

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		switch resp.Type {
		case pb.CommandResponse_STDOUT:
			outputs = append(outputs, string(resp.Payload))
		case pb.CommandResponse_EXIT:
			exitCode = string(resp.Payload)
		}
	}

	fullOutput := ""
	for _, out := range outputs {
		fullOutput += out
	}

	assert.Contains(t, fullOutput, "hello-world-test")
	assert.Equal(t, "0", exitCode)
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
