package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
)

func (s *Store) UpsertHost(ctx context.Context, h *models.Host) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hosts (id, hostname, display_name, monitor_type, device_type, os, arch, platform, kernel,
			cpu_model, cpu_cores, memory_total, ip_addresses, mac_address, vendor,
			discovered_via, site, last_seen, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname=excluded.hostname, os=excluded.os, arch=excluded.arch,
			platform=excluded.platform, kernel=excluded.kernel,
			cpu_model=excluded.cpu_model, cpu_cores=excluded.cpu_cores,
			memory_total=excluded.memory_total, ip_addresses=excluded.ip_addresses,
			mac_address=excluded.mac_address, vendor=excluded.vendor,
			site=CASE WHEN excluded.site != '' THEN excluded.site ELSE hosts.site END,
			last_seen=datetime('now'), status=excluded.status
	`, h.ID, h.Hostname, h.DisplayName, h.MonitorType, h.DeviceType, h.OS, h.Arch, h.Platform, h.Kernel,
		h.CPUModel, h.CPUCores, h.MemoryTotal, h.IPAddressesJSON(), h.MACAddress, h.Vendor,
		h.DiscoveredVia, h.Site, h.Status)
	return err
}

func (s *Store) GetHost(ctx context.Context, id string) (*models.Host, error) {
	h := &models.Host{}
	var ips string
	var firstSeen, lastSeen string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, hostname, display_name, monitor_type, device_type, os, arch, platform, kernel,
			cpu_model, cpu_cores, memory_total, ip_addresses, mac_address, vendor,
			discovered_via, site, first_seen, last_seen, status
		FROM hosts WHERE id = ?
	`, id).Scan(&h.ID, &h.Hostname, &h.DisplayName, &h.MonitorType, &h.DeviceType, &h.OS, &h.Arch,
		&h.Platform, &h.Kernel, &h.CPUModel, &h.CPUCores, &h.MemoryTotal, &ips,
		&h.MACAddress, &h.Vendor, &h.DiscoveredVia, &h.Site, &firstSeen, &lastSeen, &h.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.IPAddresses = models.ParseIPAddresses(ips)
	h.FirstSeen = models.ParseDBTime(firstSeen)
	h.LastSeen = models.ParseDBTime(lastSeen)
	return h, nil
}

func (s *Store) ListHosts(ctx context.Context) ([]models.Host, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, hostname, display_name, monitor_type, device_type, os, arch, platform, kernel,
			cpu_model, cpu_cores, memory_total, ip_addresses, mac_address, vendor,
			discovered_via, site, first_seen, last_seen, status
		FROM hosts ORDER BY site, hostname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []models.Host
	for rows.Next() {
		var h models.Host
		var ips, firstSeen, lastSeen string
		if err := rows.Scan(&h.ID, &h.Hostname, &h.DisplayName, &h.MonitorType, &h.DeviceType, &h.OS, &h.Arch,
			&h.Platform, &h.Kernel, &h.CPUModel, &h.CPUCores, &h.MemoryTotal, &ips,
			&h.MACAddress, &h.Vendor, &h.DiscoveredVia, &h.Site, &firstSeen, &lastSeen, &h.Status); err != nil {
			return nil, err
		}
		h.IPAddresses = models.ParseIPAddresses(ips)
		h.FirstSeen = models.ParseDBTime(firstSeen)
		h.LastSeen = models.ParseDBTime(lastSeen)
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func (s *Store) UpdateHostStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE hosts SET status = ?, last_seen = datetime('now') WHERE id = ?", status, id)
	return err
}

func (s *Store) UpdateHostLastSeen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE hosts SET last_seen = datetime('now') WHERE id = ?", id)
	return err
}

func (s *Store) RenameHost(ctx context.Context, id, displayName string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE hosts SET display_name = ? WHERE id = ?", displayName, id)
	return err
}

func (s *Store) UpdateHostDeviceType(ctx context.Context, id, deviceType string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE hosts SET device_type = ? WHERE id = ?", deviceType, id)
	return err
}

func (s *Store) DeleteHost(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.ExecContext(ctx, "DELETE FROM metrics WHERE host_id = ?", id)
	tx.ExecContext(ctx, "DELETE FROM services WHERE host_id = ?", id)
	tx.ExecContext(ctx, "DELETE FROM alerts WHERE host_id = ?", id)
	_, err = tx.ExecContext(ctx, "DELETE FROM hosts WHERE id = ?", id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateHostVendor(ctx context.Context, id, vendor, deviceType string) error {
	if deviceType != "" {
		_, err := s.db.ExecContext(ctx,
			"UPDATE hosts SET vendor = ?, device_type = ? WHERE id = ?", vendor, deviceType, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE hosts SET vendor = ? WHERE id = ?", vendor, id)
	return err
}

func (s *Store) HostCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosts").Scan(&count)
	return count, err
}

func (s *Store) HostCountByType(ctx context.Context, monitorType string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM hosts WHERE monitor_type = ?", monitorType).Scan(&count)
	return count, err
}

// StaleHost is a host that was marked offline.
type StaleHost struct {
	ID          string
	Hostname    string
	MonitorType string
}

// MarkStaleHostsOffline marks hosts as offline if they haven't been seen
// recently. Agent hosts are expected to heartbeat every 60s; passive
// devices are only seen by network scans (default every 5 minutes), so
// their threshold must exceed the scan interval or every device flaps
// offline between scans.
// Returns the list of hosts that transitioned from online to offline.
func (s *Store) MarkStaleHostsOffline(ctx context.Context, agentThreshold, passiveThreshold time.Duration) ([]StaleHost, error) {
	agentCutoff := time.Now().UTC().Add(-agentThreshold).Format("2006-01-02 15:04:05")
	passiveCutoff := time.Now().UTC().Add(-passiveThreshold).Format("2006-01-02 15:04:05")

	// Find hosts about to go offline
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, hostname, monitor_type FROM hosts
		WHERE status = 'online' AND (
			(monitor_type = 'passive' AND last_seen < ?) OR
			(monitor_type != 'passive' AND last_seen < ?)
		)`, passiveCutoff, agentCutoff)
	if err != nil {
		return nil, err
	}
	var stale []StaleHost
	for rows.Next() {
		var h StaleHost
		if err := rows.Scan(&h.ID, &h.Hostname, &h.MonitorType); err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, h)
	}
	rows.Close()

	if len(stale) == 0 {
		return nil, nil
	}

	// Mark them offline
	_, err = s.db.ExecContext(ctx, `
		UPDATE hosts SET status = 'offline'
		WHERE status = 'online' AND (
			(monitor_type = 'passive' AND last_seen < ?) OR
			(monitor_type != 'passive' AND last_seen < ?)
		)`, passiveCutoff, agentCutoff)
	if err != nil {
		return nil, err
	}

	return stale, nil
}
