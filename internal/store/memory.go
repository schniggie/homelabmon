package store

import (
	"context"
	"database/sql"
	"time"
)

// Memory is one persistent record about a node (or the whole homelab).
// Management actions are recorded automatically; the agent can also store
// its own notes to recall in later sessions.
type Memory struct {
	ID        int64     `json:"id"`
	HostID    string    `json:"host_id"`
	Hostname  string    `json:"hostname"`
	Scope     string    `json:"scope"` // node:<host_id> or global
	Kind      string    `json:"kind"`  // action, note, observation
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Source    string    `json:"source"` // agent, llm, user
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) InsertMemory(ctx context.Context, m *Memory) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO memories (host_id, hostname, scope, kind, title, detail, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, m.HostID, m.Hostname, m.Scope, m.Kind, m.Title, m.Detail, m.Source, m.CreatedAt.UTC())
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		m.ID = id
	}
	return nil
}

// GetMemory returns one memory entry, or nil if it does not exist.
func (s *Store) GetMemory(ctx context.Context, id int64) (*Memory, error) {
	var m Memory
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, host_id, hostname, scope, kind, title, detail, source, created_at
		FROM memories WHERE id = ?
	`, id).Scan(&m.ID, &m.HostID, &m.Hostname, &m.Scope, &m.Kind, &m.Title, &m.Detail, &m.Source, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.CreatedAt = parseTime(createdAt)
	return &m, nil
}

// UpdateMemory rewrites title and detail of an entry (user edit). Kind,
// scope, and source stay as recorded; the agent recalls the edited version.
func (s *Store) UpdateMemory(ctx context.Context, id int64, title, detail string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE memories SET title = ?, detail = ? WHERE id = ?
	`, title, detail, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteMemory removes an entry permanently.
func (s *Store) DeleteMemory(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	return err
}

// ListMemories returns memories, newest first. Filters are optional
// (empty string = no filter). hostID filter matches the node scope.
func (s *Store) ListMemories(ctx context.Context, hostID, kind string, limit int) ([]Memory, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}

	query := `SELECT id, host_id, hostname, scope, kind, title, detail, source, created_at FROM memories WHERE 1=1`
	args := []interface{}{}
	if hostID != "" {
		query += ` AND (host_id = ? OR scope = 'global')`
		args = append(args, hostID)
	}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Memory
	for rows.Next() {
		var m Memory
		var createdAt string
		if err := rows.Scan(&m.ID, &m.HostID, &m.Hostname, &m.Scope, &m.Kind, &m.Title, &m.Detail, &m.Source, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(createdAt)
		list = append(list, m)
	}
	return list, nil
}
