package challenge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/db"
	"gopkg.in/yaml.v3"
)

type Spec struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Type        string   `yaml:"type"`
	Difficulty  string   `yaml:"difficulty"`
	Tags        []string `yaml:"tags"`
	Description string   `yaml:"description"`
	Image       string   `yaml:"image"`
}

func SyncChallenges(database *db.DB, challengesDir string) error {
	entries, err := os.ReadDir(challengesDir)
	if err != nil {
		return fmt.Errorf("read challenges dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "base" {
			continue
		}
		dirPath := filepath.Join(challengesDir, entry.Name())
		yamlPath := filepath.Join(dirPath, "challenge.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", yamlPath, err)
		}

		var spec Spec
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return fmt.Errorf("parse %s: %w", yamlPath, err)
		}

		if spec.ID == "" {
			spec.ID = entry.Name()
		}
		if spec.Type == "" {
			spec.Type = "script"
		}
		if spec.Image == "" {
			spec.Image = fmt.Sprintf("breakfix-%s:dev", spec.ID)
		}

		tags, _ := json.Marshal(spec.Tags)

		c := db.Challenge{
			ID:          spec.ID,
			Title:       spec.Title,
			Type:        spec.Type,
			Difficulty:  spec.Difficulty,
			Tags:        string(tags),
			Description: spec.Description,
			Image:       spec.Image,
			DirPath:     dirPath,
		}
		if err := database.UpsertChallenge(c); err != nil {
			return fmt.Errorf("upsert %s: %w", spec.ID, err)
		}
	}
	return nil
}
