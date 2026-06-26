package pty

import (
	"io"

	pb "github.com/breakfix/breakfix/internal/proto"
)

// ReadWriter bridges a gRPC PTY stream to io.Reader+Writer.
type ReadWriter struct {
	Stream pb.Breakfix_ExecInstanceServer
}

func (rw *ReadWriter) Read(p []byte) (int, error) {
	data, err := rw.Stream.Recv()
	if err != nil {
		if err == io.EOF {
			return 0, io.EOF
		}
		return 0, err
	}
	return copy(p, data.Data), nil
}

func (rw *ReadWriter) Write(p []byte) (int, error) {
	if err := rw.Stream.Send(&pb.PTYData{Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}
