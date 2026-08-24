package store

import (
	"context"
	"time"
)

// ExecRecord is one entry in the command execution audit trail.
type ExecRecord struct {
	ID         int64     `json:"id"`
	HostID     string    `json:"host_id"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Command    string    `json:"command"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out"`
	DurationMs int64     `json:"duration_ms"`
	ExecutedAt time.Time `json:"executed_at"`
}

func (s *Store) InsertExecRecord(ctx context.Context, rec *ExecRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO exec_history (host_id, hostname, os, command, stdout, stderr, exit_code, timed_out, duration_ms, executed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.HostID, rec.Hostname, rec.OS, rec.Command, rec.Stdout, rec.Stderr,
		rec.ExitCode, boolToInt(rec.TimedOut), rec.DurationMs, rec.ExecutedAt.UTC())
	return err
}

// ListExecHistory returns the most recent executions, optionally filtered by
// host. stdout/stderr are truncated to limit for display.
func (s *Store) ListExecHistory(ctx context.Context, hostID string, limit int) ([]ExecRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}

	query := `
		SELECT id, host_id, hostname, os, command, stdout, stderr, exit_code, timed_out, duration_ms, executed_at
		FROM exec_history`
	args := []interface{}{}
	if hostID != "" {
		query += ` WHERE host_id = ?`
		args = append(args, hostID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ExecRecord
	for rows.Next() {
		var rec ExecRecord
		var timedOut int
		var executedAt string
		if err := rows.Scan(&rec.ID, &rec.HostID, &rec.Hostname, &rec.OS, &rec.Command,
			&rec.Stdout, &rec.Stderr, &rec.ExitCode, &timedOut, &rec.DurationMs, &executedAt); err != nil {
			return nil, err
		}
		rec.TimedOut = timedOut != 0
		rec.ExecutedAt = parseTime(executedAt)
		list = append(list, rec)
	}
	return list, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
