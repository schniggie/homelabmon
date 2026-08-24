package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
)

// UpsertService inserts or updates a discovered service.
func (s *Store) UpsertService(ctx context.Context, svc *models.DiscoveredService) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO services (host_id, name, port, protocol, process, category, source, container_id, container_image, stack, health, status, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(host_id, port, protocol) DO UPDATE SET
			name = excluded.name,
			process = excluded.process,
			category = excluded.category,
			source = excluded.source,
			container_id = excluded.container_id,
			container_image = excluded.container_image,
			stack = excluded.stack,
			health = excluded.health,
			status = excluded.status,
			last_seen = datetime('now')
	`, svc.HostID, svc.Name, svc.Port, svc.Protocol, svc.Process,
		svc.Category, svc.Source, svc.ContainerID, svc.ContainerImg, svc.Stack, svc.Health, svc.Status)
	return err
}

// UpsertServices inserts or updates many discovered services in a single
// transaction. Heartbeats carry a node's full service list; writing them
// one-by-one costs one fsync each, which is ~300ms on slow container
// storage -- dozens of services then exceed the peer's heartbeat timeout.
func (s *Store) UpsertServices(ctx context.Context, services []models.DiscoveredService) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO services (host_id, name, port, protocol, process, category, source, container_id, container_image, stack, health, status, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(host_id, port, protocol) DO UPDATE SET
			name = excluded.name,
			process = excluded.process,
			category = excluded.category,
			source = excluded.source,
			container_id = excluded.container_id,
			container_image = excluded.container_image,
			stack = excluded.stack,
			health = excluded.health,
			status = excluded.status,
			last_seen = datetime('now')
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i := range services {
		svc := &services[i]
		if _, err := stmt.ExecContext(ctx, svc.HostID, svc.Name, svc.Port, svc.Protocol, svc.Process,
			svc.Category, svc.Source, svc.ContainerID, svc.ContainerImg, svc.Stack, svc.Health, svc.Status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListServicesByHost returns all active services for a host.
func (s *Store) ListServicesByHost(ctx context.Context, hostID string) ([]models.DiscoveredService, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, host_id, name, port, protocol, process, category, source,
		       container_id, container_image, stack, health, status, first_seen, last_seen
		FROM services WHERE host_id = ? ORDER BY stack, name, port
	`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServices(rows)
}

// ListAllServices returns all services across all hosts.
func (s *Store) ListAllServices(ctx context.Context) ([]models.DiscoveredService, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, host_id, name, port, protocol, process, category, source,
		       container_id, container_image, stack, health, status, first_seen, last_seen
		FROM services ORDER BY name, port
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServices(rows)
}

// ServiceCountByHost returns the count of active services per host.
func (s *Store) ServiceCountByHost(ctx context.Context, hostID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE host_id = ? AND status = 'active'`, hostID).Scan(&count)
	return count, err
}

// MarkStaleServicesGone marks services not seen recently as "gone".
func (s *Store) MarkStaleServicesGone(ctx context.Context, staleAfter time.Duration) error {
	cutoff := time.Now().UTC().Add(-staleAfter)
	_, err := s.db.ExecContext(ctx, `
		UPDATE services SET status = 'gone'
		WHERE status = 'active' AND last_seen < ?
	`, cutoff)
	return err
}

func scanServices(rows *sql.Rows) ([]models.DiscoveredService, error) {
	var services []models.DiscoveredService
	for rows.Next() {
		var svc models.DiscoveredService
		err := rows.Scan(
			&svc.ID, &svc.HostID, &svc.Name, &svc.Port, &svc.Protocol,
			&svc.Process, &svc.Category, &svc.Source,
			&svc.ContainerID, &svc.ContainerImg, &svc.Stack, &svc.Health,
			&svc.Status, &svc.FirstSeen, &svc.LastSeen,
		)
		if err != nil {
			return nil, err
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}
