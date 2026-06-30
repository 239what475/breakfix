package server

import (
	"io"
	"sync"

	pb "github.com/breakfix/breakfix/pkg/proto"
	"k8s.io/client-go/tools/remotecommand"
)

// ReadWriter bridges a gRPC PTY stream to io.Reader+Writer.
// Resize messages (Cols/Rows > 0) are sent to the Resize channel instead.
type ReadWriter struct {
	Stream pb.Breakfix_ExecInstanceServer
	Resize chan remotecommand.TerminalSize
	mu     sync.Mutex
}

func (rw *ReadWriter) Read(p []byte) (int, error) {
	for {
		data, err := rw.Stream.Recv()
		if err != nil {
			if err == io.EOF {
				return 0, io.EOF
			}
			return 0, err
		}
		// Resize message
		if data.Cols > 0 || data.Rows > 0 {
			if rw.Resize != nil {
				rw.Resize <- remotecommand.TerminalSize{
					Width:  uint16(data.Cols),
					Height: uint16(data.Rows),
				}
			}
			continue
		}
		return copy(p, data.Data), nil
	}
}

func (rw *ReadWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if err := rw.Stream.Send(&pb.PTYData{Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}
