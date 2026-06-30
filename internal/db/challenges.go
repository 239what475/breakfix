package db

import "fmt"

type Challenge struct {
	ID          string
	Title       string
	Type        string
	Difficulty  string
	Tags        string // JSON array
	Description string
	Image       string
	DirPath     string // filesystem path (pre-authored challenges)
	CreatedAt   string
}

func (d *DB) UpsertChallenge(c Challenge) error {
	_, err := d.conn.Exec(
		`INSERT INTO challenges (id, title, type, difficulty, tags, description, image, dir_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   title=excluded.title, type=excluded.type, difficulty=excluded.difficulty,
		   tags=excluded.tags, description=excluded.description,
		   image=excluded.image, dir_path=excluded.dir_path`,
		c.ID, c.Title, c.Type, c.Difficulty, c.Tags, c.Description,
		c.Image, c.DirPath,
	)
	return err
}

func (d *DB) GetChallenge(id string) (*Challenge, error) {
	c := &Challenge{}
	err := d.conn.QueryRow(
		`SELECT id, title, type, difficulty, tags, description, image, dir_path, created_at
		 FROM challenges WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.Title, &c.Type, &c.Difficulty, &c.Tags, &c.Description,
		&c.Image, &c.DirPath, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get challenge %s: %w", id, err)
	}
	return c, nil
}

func (d *DB) ListChallenges() ([]Challenge, error) {
	rows, err := d.conn.Query(
		"SELECT id, title, type, difficulty, tags, description, image, dir_path, created_at FROM challenges ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var cs []Challenge
	for rows.Next() {
		var c Challenge
		if err := rows.Scan(&c.ID, &c.Title, &c.Type, &c.Difficulty, &c.Tags, &c.Description,
			&c.Image, &c.DirPath, &c.CreatedAt); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}
