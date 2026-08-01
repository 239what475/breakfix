package taxonomy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	revisionsDirectory = "revisions"
	currentPointer     = "current"
)

// Store owns filesystem publication of immutable taxonomy snapshots. It has no
// database dependency: the filesystem is the taxonomy authority.
type Store struct {
	root string
}

func NewStore(dataDir string) *Store {
	return &Store{root: filepath.Join(dataDir, "taxonomy")}
}

func (s *Store) Root() string { return s.root }

func (s *Store) CurrentPath() string { return filepath.Join(s.root, currentPointer) }

func (s *Store) RevisionsPath() string { return filepath.Join(s.root, revisionsDirectory) }

func (s *Store) LoadCurrent() (*Snapshot, error) {
	target, err := s.currentTarget()
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(target) || filepath.Clean(target) != filepath.Join(revisionsDirectory, filepath.Base(target)) {
		return nil, fmt.Errorf("taxonomy current pointer has invalid target %q", target)
	}
	revision := filepath.Base(target)
	if !validRevision(revision) {
		return nil, fmt.Errorf("taxonomy current pointer has invalid revision %q", revision)
	}
	return s.LoadRevision(revision)
}

// currentTarget reads the current snapshot pointer. Runtime publication uses a
// symlink so replacement is atomic. A checked-in catalog seed may use a plain
// text pointer instead because Git does not preserve empty directories needed
// by an otherwise valid snapshot without skill prerequisite mappings.
func (s *Store) currentTarget() (string, error) {
	info, err := os.Lstat(s.CurrentPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoCurrentRevision
		}
		return "", fmt.Errorf("stat taxonomy current pointer: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(s.CurrentPath())
		if err != nil {
			return "", fmt.Errorf("read taxonomy current pointer: %w", err)
		}
		return target, nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("taxonomy current pointer must be a symlink or regular file")
	}
	data, err := os.ReadFile(s.CurrentPath())
	if err != nil {
		return "", fmt.Errorf("read taxonomy seed pointer: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *Store) LoadRevision(revision string) (*Snapshot, error) {
	if !validRevision(revision) {
		return nil, fmt.Errorf("invalid taxonomy revision %q", revision)
	}
	dir := filepath.Join(s.RevisionsPath(), revision)
	loaded, err := loadSnapshot(dir)
	if err != nil {
		return nil, err
	}
	hash, err := treeRevision(dir)
	if err != nil {
		return nil, err
	}
	if hash != revision {
		return nil, fmt.Errorf("taxonomy revision directory %q hashes to %q", revision, hash)
	}
	loaded.Revision = revision
	if err := Validate(*loaded); err != nil {
		return nil, fmt.Errorf("validate taxonomy revision %q: %w", revision, err)
	}
	return loaded, nil
}

// Publish validates and writes a complete snapshot, then atomically makes it
// current. The returned revision is the content hash of the complete tree.
// A snapshot that already exists is reused rather than rewritten.
func (s *Store) Publish(snapshot Snapshot) (*Snapshot, error) {
	if err := Validate(snapshot); err != nil {
		return nil, fmt.Errorf("validate taxonomy snapshot: %w", err)
	}
	if err := os.MkdirAll(s.RevisionsPath(), 0755); err != nil {
		return nil, fmt.Errorf("create taxonomy revisions directory: %w", err)
	}
	staging, err := os.MkdirTemp(s.RevisionsPath(), ".tmp-")
	if err != nil {
		return nil, fmt.Errorf("create taxonomy staging directory: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck
	if err := writeSnapshot(staging, snapshot); err != nil {
		return nil, err
	}
	revision, err := treeRevision(staging)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(s.RevisionsPath(), revision)
	if _, err := os.Stat(target); err == nil {
		if _, err := s.LoadRevision(revision); err != nil {
			return nil, fmt.Errorf("read existing taxonomy revision: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat taxonomy revision: %w", err)
	} else if err := os.Rename(staging, target); err != nil {
		if _, statErr := os.Stat(target); statErr != nil {
			return nil, fmt.Errorf("publish taxonomy revision: %w", err)
		}
		if _, loadErr := s.LoadRevision(revision); loadErr != nil {
			return nil, fmt.Errorf("read concurrently published taxonomy revision: %w", loadErr)
		}
	}
	if err := s.replaceCurrent(revision); err != nil {
		return nil, err
	}
	return s.LoadRevision(revision)
}

// PreviewRevision computes the immutable revision that Publish would create
// without changing current. Server records this value before publication so a
// restart can determine whether the filesystem side effect already happened.
func (s *Store) PreviewRevision(snapshot Snapshot) (string, error) {
	if err := Validate(snapshot); err != nil {
		return "", fmt.Errorf("validate taxonomy snapshot: %w", err)
	}
	if err := os.MkdirAll(s.RevisionsPath(), 0755); err != nil {
		return "", fmt.Errorf("create taxonomy revisions directory: %w", err)
	}
	staging, err := os.MkdirTemp(s.RevisionsPath(), ".preview-")
	if err != nil {
		return "", fmt.Errorf("create taxonomy preview directory: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck
	if err := writeSnapshot(staging, snapshot); err != nil {
		return "", err
	}
	return treeRevision(staging)
}

func (s *Store) replaceCurrent(revision string) error {
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return fmt.Errorf("create taxonomy root: %w", err)
	}
	temporary := filepath.Join(s.root, ".current-"+strings.TrimPrefix(revision, "sha256:")[:12])
	_ = os.Remove(temporary)
	if err := os.Symlink(filepath.Join(revisionsDirectory, revision), temporary); err != nil {
		return fmt.Errorf("create taxonomy current pointer: %w", err)
	}
	if err := os.Rename(temporary, s.CurrentPath()); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace taxonomy current pointer: %w", err)
	}
	return nil
}

func loadSnapshot(root string) (*Snapshot, error) {
	requiredDirs := []string{
		filepath.Join(root, "skills"),
		filepath.Join(root, "tags"),
		filepath.Join(root, "mappings", "challenges"),
	}
	for _, dir := range requiredDirs {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("read taxonomy directory %s: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("taxonomy path %s must be a directory", dir)
		}
	}
	if err := validateSnapshotTree(root); err != nil {
		return nil, err
	}

	snapshot := &Snapshot{}
	var err error
	if snapshot.Skills, err = readDefinitions[Skill](filepath.Join(root, "skills"), func(value *Skill, file string) { value.File = file }); err != nil {
		return nil, err
	}
	if snapshot.Tags, err = readDefinitions[Tag](filepath.Join(root, "tags"), func(value *Tag, file string) { value.File = file }); err != nil {
		return nil, err
	}
	if snapshot.ChallengeMappings, err = readDefinitions[ChallengeMapping](filepath.Join(root, "mappings", "challenges"), func(value *ChallengeMapping, file string) { value.File = file }); err != nil {
		return nil, err
	}
	if snapshot.SkillMappings, err = readOptionalDefinitions[SkillMapping](filepath.Join(root, "mappings", "skills"), func(value *SkillMapping, file string) { value.File = file }); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func validateSnapshotTree(root string) error {
	allowedDirectories := map[string]bool{
		".":                   true,
		"skills":              true,
		"tags":                true,
		"mappings":            true,
		"mappings/challenges": true,
		"mappings/skills":     true,
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("taxonomy snapshot does not allow symlink %s", rel)
		}
		if entry.IsDir() {
			if !allowedDirectories[rel] {
				return fmt.Errorf("unexpected taxonomy directory %s", rel)
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") || !entry.Type().IsRegular() {
			return fmt.Errorf("taxonomy snapshot only allows YAML files, got %s", rel)
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		if parent != "skills" && parent != "tags" && parent != "mappings/challenges" && parent != "mappings/skills" {
			return fmt.Errorf("unexpected taxonomy file %s", rel)
		}
		if !challengeFileStem(strings.TrimSuffix(entry.Name(), ".yaml")) {
			return fmt.Errorf("invalid taxonomy source filename %s", rel)
		}
		return nil
	})
}

func readDefinitions[T any](dir string, setFile func(*T, string)) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var value T
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("parse taxonomy file %s: %w", entry.Name(), err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, fmt.Errorf("taxonomy file %s has multiple YAML documents", entry.Name())
			}
			return nil, fmt.Errorf("parse taxonomy file %s: %w", entry.Name(), err)
		}
		setFile(&value, strings.TrimSuffix(entry.Name(), ".yaml"))
		values = append(values, value)
	}
	return values, nil
}

func readOptionalDefinitions[T any](dir string, setFile func(*T, string)) ([]T, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return readDefinitions(dir, setFile)
}

func writeSnapshot(root string, snapshot Snapshot) error {
	for _, dir := range []string{"skills", "tags", filepath.Join("mappings", "challenges"), filepath.Join("mappings", "skills")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			return fmt.Errorf("create taxonomy snapshot directory: %w", err)
		}
	}
	snapshot = snapshot.Sorted()
	if err := writeDefinitions(filepath.Join(root, "skills"), snapshot.Skills, func(value Skill) (string, string, string) {
		return value.File, value.Title, value.ID
	}); err != nil {
		return err
	}
	if err := writeDefinitions(filepath.Join(root, "tags"), snapshot.Tags, func(value Tag) (string, string, string) {
		return value.File, value.Title, value.ID
	}); err != nil {
		return err
	}
	if err := writeDefinitions(filepath.Join(root, "mappings", "challenges"), snapshot.ChallengeMappings, func(value ChallengeMapping) (string, string, string) {
		return value.File, value.Challenge.Title, value.Challenge.ID
	}); err != nil {
		return err
	}
	return writeDefinitions(filepath.Join(root, "mappings", "skills"), snapshot.SkillMappings, func(value SkillMapping) (string, string, string) {
		return value.File, value.Source.Title, value.Source.ID
	})
}

func writeDefinitions[T any](dir string, values []T, name func(T) (string, string, string)) error {
	used := make(map[string]struct{}, len(values))
	for _, value := range values {
		file, title, id := name(value)
		stem := sourceFileStem(file, title, id, used)
		if _, exists := used[stem]; exists {
			return fmt.Errorf("duplicate taxonomy source filename %q", stem)
		}
		used[stem] = struct{}{}
		data, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, stem+".yaml"), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func sourceFileStem(existing, title, id string, used map[string]struct{}) string {
	// A previous ASCII-only writer could fall back to an opaque object ID. It
	// carries no source-level meaning, so migrate it to the readable title stem
	// the next time this immutable snapshot is published.
	if challengeFileStem(existing) && existing != id {
		return existing
	}
	stem := slug(title)
	if !challengeFileStem(stem) {
		stem = id
	}
	if _, exists := used[stem]; !exists {
		return stem
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", stem, index)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func slug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(unicode.ToLower(r))
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func challengeFileStem(value string) bool {
	runes := []rune(value)
	if len(runes) == 0 {
		return false
	}
	for index, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		if r == '-' && index > 0 && index < len(runes)-1 {
			continue
		}
		return false
	}
	return true
}

func treeRevision(root string) (string, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("taxonomy snapshot has unsupported file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", err
	}
	slices.Sort(files)
	hash := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
