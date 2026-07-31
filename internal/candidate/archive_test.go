package candidate

import (
	"errors"
	"os"
	"testing"
)

func TestSaveArchiveAtomicAcceptsOnlyIdenticalRetry(t *testing.T) {
	root := t.TempDir()
	path, digest, err := SaveArchiveAtomic(root, "candidate-0123456789abcdef", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if digest != Digest([]byte("first")) || path != ArchivePath(root, "candidate-0123456789abcdef") {
		t.Fatalf("stored path/digest = %q %q", path, digest)
	}
	if _, _, err := SaveArchiveAtomic(root, "candidate-0123456789abcdef", []byte("first")); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if _, _, err := SaveArchiveAtomic(root, "candidate-0123456789abcdef", []byte("different")); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("conflicting retry = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("archive remains writable: mode=%o", info.Mode().Perm())
	}
	if _, err := ReadArchive(path, digest); err != nil {
		t.Fatalf("read immutable archive: %v", err)
	}
}

func TestIDForGeneratorRunIsOpaqueAndStable(t *testing.T) {
	first := IDForGeneratorRun("generator-run-one")
	if first != IDForGeneratorRun("generator-run-one") || first == IDForGeneratorRun("generator-run-two") {
		t.Fatalf("candidate IDs are not stable and unique: %q", first)
	}
}
