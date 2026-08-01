package incus

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ImageFilesFromDirectory returns the complete regular-file bundle used by a
// stopped Node image build. It rejects links and special files so callers
// cannot make the resulting image depend on host filesystem behavior.
func ImageFilesFromDirectory(root string) ([]ImageFile, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFS.Close() //nolint:errcheck

	files := make([]ImageFile, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("image bundle contains unsupported entry %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file, err := rootFS.Open(relative)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, ImageFile{
			Path:    filepath.ToSlash(relative),
			Content: content,
			Mode:    int(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("image bundle is empty")
	}
	return files, nil
}
