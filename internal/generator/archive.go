package generator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func (g *Generator) uploadArtifact(ctx context.Context, chalDir string) error {
	if strings.TrimSpace(g.GenerationID) == "" {
		return fmt.Errorf("GENERATION_ID is required")
	}
	if strings.TrimSpace(g.ServerURL) == "" {
		return fmt.Errorf("SERVER_INTERNAL_URL is required")
	}
	if strings.TrimSpace(g.InternalAPIKey) == "" {
		return fmt.Errorf("SERVER_INTERNAL_API_KEY is required")
	}
	if err := g.validateChallengeManifest(chalDir); err != nil {
		return err
	}

	payload, err := archiveDir(chalDir)
	if err != nil {
		return err
	}
	slog.Info("artifact archived", "generationID", g.GenerationID, "artifactID", g.artifactID, "bytes", len(payload))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("artifact", g.artifactID+".tar.gz")
	if err != nil {
		return fmt.Errorf("create multipart artifact part: %w", err)
	}
	if _, err := part.Write(payload); err != nil {
		return fmt.Errorf("write artifact payload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart body: %w", err)
	}

	url := strings.TrimRight(g.ServerURL, "/") + "/api/internal/generations/" + g.GenerationID + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Breakfix-Internal-Key", g.InternalAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upload artifact: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	slog.Info("artifact uploaded", "generationID", g.GenerationID, "artifactID", g.artifactID, "status", resp.StatusCode)
	return nil
}

func downloadSubmission(ctx context.Context, serverURL, internalAPIKey, submissionID, dst string) error {
	url := strings.TrimRight(serverURL, "/") + "/api/internal/verify-submissions/" + submissionID + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Breakfix-Internal-Key", internalAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download submission: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func archiveDir(root string) ([]byte, error) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive does not allow symlink %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("archive does not allow non-regular file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative archive path for %s: %w", path, err)
		}
		name := filepath.ToSlash(rel)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("build tar header for %s: %w", path, err)
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		} else {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header for %s: %w", path, err)
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("write tar body for %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("archive challenge dir: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar stream: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip stream: %w", err)
	}
	return buf.Bytes(), nil
}

func ensureClaudeWorkspaceWritable(path string) error {
	usr, err := user.Lookup("node")
	if err != nil {
		return nil
	}
	uid, err := strconv.Atoi(usr.Uid)
	if err != nil {
		return fmt.Errorf("parse node uid: %w", err)
	}
	gid, err := strconv.Atoi(usr.Gid)
	if err != nil {
		return fmt.Errorf("parse node gid: %w", err)
	}
	return filepath.Walk(path, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(current, uid, gid)
	})
}
