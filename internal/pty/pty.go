package pty

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	pb "github.com/breakfix/breakfix/internal/proto"
	"k8s.io/klog/v2"
)

// Proxy forwards between a gRPC PTY stream and kubectl exec.
func Proxy(stream pb.Breakfix_ExecInstanceServer, namespace, podName string) error {
	cmd := exec.Command("kubectl", "exec", "-ti", "-n", namespace, podName, "--", "/bin/bash") //nolint:gosec
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start exec: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})

	// stdout → gRPC stream
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&pb.PTYData{Data: buf[:n]}); sendErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// stderr → gRPC stream
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				stream.Send(&pb.PTYData{Data: buf[:n]}) //nolint:errcheck
			}
			if err != nil {
				return
			}
		}
	}()

	// gRPC stream → stdin
	go func() {
		for {
			data, err := stream.Recv()
			if err != nil {
				close(done)
				return
			}
			if len(data.Data) > 0 {
				stdin.Write(data.Data) //nolint:errcheck
			}
		}
	}()

	// Handle window resize signals
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		for range sigCh {
			// In a real PTY implementation, we'd send SIGWINCH to the
			// kubectl exec process. For now this is best-effort.
			if cmd.Process != nil {
				cmd.Process.Signal(syscall.SIGWINCH) //nolint:errcheck
			}
		}
	}()

	<-done
	stdin.Close()
	cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck
	wg.Wait()
	cmd.Wait() //nolint:errcheck
	klog.V(2).InfoS("pty session ended", "pod", podName)
	return nil
}
