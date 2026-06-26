package db

import (
	"fmt"
)

type Instance struct {
	ID          string
	UserID      string
	ChallengeID string
	Status      string // running | draining | destroyed
	Namespace   string
	PodName     string
	CreatedAt   string
}

func (d *DB) CreateInstance(i Instance) error {
	_, err := d.conn.Exec(
		`INSERT INTO instances (id, user_id, challenge_id, status, namespace, pod_name)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		i.ID, i.UserID, i.ChallengeID, i.Status, i.Namespace, i.PodName,
	)
	return err
}

func (d *DB) GetInstance(id string) (*Instance, error) {
	i := &Instance{}
	err := d.conn.QueryRow(
		"SELECT id, user_id, challenge_id, status, namespace, pod_name, created_at FROM instances WHERE id = ?",
		id,
	).Scan(&i.ID, &i.UserID, &i.ChallengeID, &i.Status, &i.Namespace, &i.PodName, &i.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get instance %s: %w", id, err)
	}
	return i, nil
}

func (d *DB) ListActiveInstances(userID string) ([]Instance, error) {
	rows, err := d.conn.Query(
		"SELECT id, user_id, challenge_id, status, namespace, pod_name, created_at FROM instances WHERE user_id = ? AND status IN ('running','draining') ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var insts []Instance
	for rows.Next() {
		var i Instance
		if err := rows.Scan(&i.ID, &i.UserID, &i.ChallengeID, &i.Status, &i.Namespace, &i.PodName, &i.CreatedAt); err != nil {
			return nil, err
		}
		insts = append(insts, i)
	}
	return insts, rows.Err()
}

func (d *DB) UpdateInstanceStatus(id, status string) error {
	_, err := d.conn.Exec(
		"UPDATE instances SET status = ? WHERE id = ?", status, id,
	)
	return err
}

func (d *DB) DestroyInstance(id string) error {
	_, err := d.conn.Exec(
		"UPDATE instances SET status = 'destroyed', destroyed_at = datetime('now') WHERE id = ?", id,
	)
	return err
}

func (d *DB) ListDrainingInstances() ([]Instance, error) {
	rows, err := d.conn.Query(
		"SELECT id, user_id, challenge_id, status, namespace, pod_name, created_at FROM instances WHERE status = 'draining'",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var insts []Instance
	for rows.Next() {
		var i Instance
		if err := rows.Scan(&i.ID, &i.UserID, &i.ChallengeID, &i.Status, &i.Namespace, &i.PodName, &i.CreatedAt); err != nil {
			return nil, err
		}
		insts = append(insts, i)
	}
	return insts, rows.Err()
}
