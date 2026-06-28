package db

import "fmt"

type Submission struct {
	ID          string
	InstanceID  string
	UserID      string
	ChallengeID string
	Passed      bool
	ExitCode    int
	Output      string
	CreatedAt   string
}

func (d *DB) CreateSubmission(s Submission) error {
	passed := 0
	if s.Passed {
		passed = 1
	}
	id := fmt.Sprintf("s-%s", s.InstanceID)
	_, err := d.conn.Exec(
		`INSERT INTO submissions (id, instance_id, user_id, challenge_id, passed, exit_code, output)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, s.InstanceID, s.UserID, s.ChallengeID, passed, s.ExitCode, s.Output,
	)
	return err
}
