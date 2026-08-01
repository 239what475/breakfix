package kubernetes

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (c *Client) CopyDirFromPod(namespace, podName, remoteDir, localDir string) error {
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}

	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"tar", "-C", remoteDir, "-cf", "-", "."},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	reader, writer := io.Pipe()
	var stderr strings.Builder
	streamErr := make(chan error, 1)

	go func() {
		streamErr <- exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
			Stdout: writer,
			Stderr: &stderr,
		})
		_ = writer.Close()
	}()

	if err := untarInto(localDir, reader); err != nil {
		_ = reader.Close()
		<-streamErr
		return err
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}
	if err := <-streamErr; err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("copy dir: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("copy dir: %w", err)
	}
	return nil
}

func untarInto(root string, stream io.Reader) error {
	tr := tar.NewReader(stream)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar stream: %w", err)
		}

		cleanName := filepath.Clean(hdr.Name)
		if cleanName == "." {
			continue
		}
		if cleanName == ".." || strings.HasPrefix(cleanName, "../") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("invalid tar path %q", hdr.Name)
		}

		target := filepath.Join(root, cleanName)
		rel, err := filepath.Rel(root, target)
		if err != nil {
			return fmt.Errorf("resolve target path: %w", err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("tar path escapes destination: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create dir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("create parent dir: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			//nolint:gosec // The platform does not impose an additional OCI archive size limit here.
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return fmt.Errorf("write file %s: %w", target, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", target, err)
			}
		default:
			return fmt.Errorf("unsupported tar entry type %d for %s", hdr.Typeflag, hdr.Name)
		}
	}
}
