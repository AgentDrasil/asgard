package fakebash

import (
	"context"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/lib/fakebash/pb"
)

func TestFakebashGRPC(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)

	clientFile := os.NewFile(uintptr(fds[0]), "client")
	serverFile := os.NewFile(uintptr(fds[1]), "server")
	defer func() { _ = clientFile.Close() }()
	defer func() { _ = serverFile.Close() }()

	clientConn, err := net.FileConn(clientFile)
	require.NoError(t, err)
	defer func() { _ = clientConn.Close() }()

	serverConn, err := net.FileConn(serverFile)
	require.NoError(t, err)
	defer func() { _ = serverConn.Close() }()

	// Start the gRPC server on the server socket
	listener := NewSingleConnListener(serverConn)
	grpcServer := grpc.NewServer()
	srv := &fakebashServer{}
	pb.RegisterFakebashServiceServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	defer grpcServer.Stop()

	// Set up client dialer
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return clientConn, nil
	}

	grpcConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
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
