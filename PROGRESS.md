# HomelabMon - Phase & Progress Tracker

## Phase Overview

| Phase | Name | Status | Description |
|-------|------|--------|-------------|
| 1 | Foundation | `COMPLETE` | Scaffolding, plugin system, SQLite, core observers, mesh, basic web UI |
| 2 | Service Discovery | `COMPLETE` | Port/process probes, fingerprinting, Docker observer, service CMDB |
| 3 | Network Discovery | `COMPLETE` | ARP scanner, mDNS browser, OUI lookup, passive devices, notifications |
| 4 | Intelligence | `COMPLETE` | Ollama LLM integration, tool-calling chat, natural language CMDB queries |
| 4b | Charts & Device Mgmt | `COMPLETE` | Inline statistics (Chart.js), device category/delete, display names |
| 5 | Integrations | `COMPLETE` | FRITZ!Box, Unifi, HA, Pi-hole, pfSense, SNMP scanner, external plugins |
| 6 | Security & Polish | `COMPLETE` | mTLS, enrollment, auth, install scripts, systemd/launchd/Windows service, config hot-reload, peer federation |
| 7 | LLM Management Agent | `COMPLETE` | Full platform tool access for the chat agent: docker control across the mesh, scans, notifications, settings, host/integration/peer management with confirmation gates |

---

## Phase 1: Foundation

**Goal:** Working mesh of nodes with system metrics, SQLite storage, and web dashboard.

### 1.1 Project Scaffolding
- [x]`go mod init` + dependencies (cobra, viper, gopsutil, sqlite, mdns, uuid, zerolog)
- [x]`main.go` entry point
- [x]`Makefile` with cross-compile targets (6 platforms)
- [x]`cmd/root.go` -- cobra root, viper config init
- [x]`cmd/run.go` -- main command with flags (--ui, --llm, --peer, --bind, --scan, --collect-interval)
- [x]`cmd/setup.go` -- create data dir, generate UUID, write default config
- [x]`cmd/version.go` -- version via ldflags

### 1.2 Data Models
- [x]`internal/models/host.go` -- Host struct (unified: agent/passive/integration)
- [x]`internal/models/metric.go` -- MetricSnapshot, DiskUsage
- [x]`internal/models/service.go` -- DiscoveredService
- [x]`internal/models/alert.go` -- Alert
- [x]`internal/models/peer.go` -- PeerInfo, Heartbeat, NodeIdentity
- [x]`internal/models/plugin.go` -- PluginType, PluginInfo

### 1.3 Plugin System
- [x]`internal/plugin/types.go` -- Plugin base interface + Observer/Probe/Scanner/Integration interfaces
- [x]`internal/plugin/registry.go` -- PluginRegistry (register, detect, start, stop, lifecycle)
- [x]`internal/plugin/event.go` -- Event struct

### 1.4 SQLite Store
- [x]`internal/store/store.go` -- connection, WAL mode, init, close
- [x]`internal/store/migrate.go` -- embedded migration runner
- [x]`internal/store/migrations/001_initial.sql` -- hosts, metrics, services, alerts, peers, plugins
- [x]`internal/store/hosts.go` -- UpsertHost, GetHost, ListHosts, UpdateHostStatus
- [x]`internal/store/metrics.go` -- InsertMetric, GetLatestMetric, GetMetricHistory, PurgeOldMetrics
- [x]`internal/store/peers.go` -- UpsertPeer, ListPeers, GetPeer

### 1.5 Core Observers (Phase 1 plugins)
- [x]`internal/agent/observers/cpu.go` -- CPU %, load avg (gopsutil)
- [x]`internal/agent/observers/memory.go` -- RAM/swap (gopsutil)
- [x]`internal/agent/observers/disk.go` -- partitions, usage (gopsutil)
- [x]`internal/agent/observers/network.go` -- I/O counters, delta (gopsutil)

### 1.6 Agent Core
- [x]`internal/agent/collector.go` -- orchestrate observers, merge results, store metrics
- [x]`internal/agent/hostinfo.go` -- one-time host info collection
- [x]`internal/agent/heartbeat.go` -- periodic heartbeat to peers

### 1.7 Mesh Transport
- [x]`internal/mesh/transport.go` -- HTTP server + client
- [x]`internal/mesh/handlers.go` -- /api/v1/register, /heartbeat, /status, /peers, /hosts, /metrics/*

### 1.8 Web UI
- [x]`web/embed.go` -- go:embed directives
- [x]`web/templates/layout.html` -- base layout (Tailwind CDN, Font Awesome, htmx, Alpine.js)
- [x]`web/templates/dashboard.html` -- node grid with status cards
- [x]`web/templates/host.html` -- host detail page
- [x]`web/static/js/app.js` -- minimal JS
- [x]`internal/hub/api/server.go` -- UI server, template parsing
- [x]`internal/hub/api/pages.go` -- dashboard + host detail handlers

### 1.9 Wiring
- [x]`cmd/run.go` -- wire everything: store -> collector -> transport -> heartbeat -> ui
- [x]Graceful shutdown (SIGINT/SIGTERM)
- [x]Background metric purge goroutine (7-day retention)

### 1.10 Verification
- [x] `go build` compiles cleanly (21.5 MB binary)
- [x] `homelabmon version` works
- [x] Single node starts, collects metrics, stores in SQLite
- [x] `--ui` serves dashboard showing local node
- [x] Two nodes discover each other via `--peer`, dashboard shows both
- [x] Cross-compilation verified (linux/amd64)
- [x] Chrome DevTools MCP: dashboard + host detail render without errors, zero JS errors
- [x] Deployed to Ubuntu server (192.168.178.45) as systemd service, mesh confirmed working

### 1.11 Bugs Fixed
- Template name collision: both dashboard.html and host.html defined `{{define "content"}}` -- last parsed wins. Fixed by parsing each page template separately with layout.
- Viper flag binding: `--bind :9601` ignored on `run` subcommand because viper was bound to rootCmd's regular Flags. Fixed by using PersistentFlags on rootCmd.
- memory_total=0: hostinfo.go was missing explicit `mem.VirtualMemory()` call.

---

## Phase 2: Service Discovery

**Goal:** Auto-discover running services via port/process fingerprinting.

- [x] `internal/agent/observers/ports.go` -- listening ports observer (gopsutil net.Connections)
- [x] `internal/agent/observers/processes.go` -- process list observer
- [x] `configs/fingerprints.yaml` -- 60+ homelab service signatures with categories
- [x] `internal/agent/discovery/engine.go` -- match ports+processes to fingerprints, store services
- [x] `internal/agent/discovery/fingerprint.go` -- YAML rule loader + matcher
- [x] `internal/agent/observers/docker.go` -- Docker API via unix socket/named pipe (no SDK)
- [x] `internal/store/services.go` -- UpsertService, ListServicesByHost, ListAllServices, MarkStaleServicesGone
- [x] `internal/store/migrations/002_services_v2.sql` -- category, source, container fields
- [x] `configs/embed.go` -- embedded fingerprints.yaml
- [x] Service CMDB population from fingerprints + Docker containers
- [x] Dashboard: service badges on host cards (deduplicated by name)
- [x] Dashboard: fleet-wide services page (`/ui/services`) with table view
- [x] Host detail: discovered services section with Docker image + port info
- [x] API: `/api/v1/services`, `/api/v1/services/{host_id}`
- [x] Verified: Main16GB (uptime-kuma, ollama), Ubuntu (13 Docker containers discovered)

---

## Phase 3: Network Discovery + Notifications

**Goal:** Discover passive devices on LAN, notify on status changes.

### 3.1 Docker on Windows Fix
- [x] Windows named pipe: `go-winio` for `//./pipe/dockerDesktopLinuxEngine`
- [x] Docker API bumped to v1.47 (Docker Desktop requires >= 1.44)
- [x] Platform-specific files: `docker_windows.go` (go-winio), `docker_unix.go` (net.Dial unix)

### 3.2 Network Scanners
- [x] `internal/agent/scanners/arp.go` -- ARP table reader (cross-platform: `/proc/net/arp` Linux, `arp -a` Windows/macOS)
- [x] `internal/agent/scanners/arp_linux.go` / `arp_other.go` -- platform-specific file opener
- [x] `internal/agent/scanners/mdns.go` -- mDNS/Bonjour browser (13 service types: airplay, googlecast, printer, homekit, spotify, etc.)
- [x] `internal/agent/scanners/oui.go` -- embedded OUI database (800+ MAC prefixes, 30+ manufacturers)
- [x] OUI vendor lookup + device type classification by vendor name
- [x] mDNS device classification by service type (media, tv, printer, phone)
- [x] Multicast/broadcast address filtering (224.x, 239.x, 01:00:5e:*, ff:ff:ff:*)
- [x] `--scan` flag activates scanners, scan every 3 minutes

### 3.3 Passive Device Store
- [x] `internal/store/devices.go` -- UpsertPassiveDevice, ListPassiveDevices
- [x] Deterministic device IDs from MAC (SHA256-based)
- [x] Merge ARP + mDNS results by MAC address (prefer richer data)
- [x] Hosts table: `monitor_type='passive'` for scanned devices

### 3.4 Notifications
- [x] `internal/notify/notifier.go` -- Notification struct, Dispatcher with dedup cooldown (10 min)
- [x] `internal/notify/ntfy.go` -- ntfy.sh sender (with priority + tags per severity)
- [x] `internal/notify/webhook.go` -- generic webhook (Discord/Slack/custom JSON)
- [x] `--notify-ntfy <url>` flag for ntfy.sh topic
- [x] `--notify-webhook <url>` flag for webhook endpoint
- [x] Host offline alerts: automatic when `MarkStaleHostsOffline` transitions hosts
- [x] Resource threshold alerts: CPU/memory/disk % configurable via flags
- [x] `--notify-cpu-threshold`, `--notify-mem-threshold`, `--notify-disk-threshold` (default 90%)
- [x] Dedup: same notification suppressed for 10 minutes to prevent spam

### 3.5 Dashboard UI
- [x] Dashboard: agent nodes separated from passive devices
- [x] Dashboard: compact network devices grid with device type icons + vendor
- [x] `web/templates/devices.html` -- full devices page with MAC, vendor, discovery source, timestamps
- [x] `/ui/devices` page with auto-refresh (htmx every 30s)
- [x] `web/templates/settings.html` -- notification config, thresholds, node info
- [x] `/ui/settings` page with "Send Test" button (htmx POST)
- [x] Nav: Services, Devices, Settings links
- [x] `deviceIcon` template function (phone, tablet, tv, printer, router, iot, etc.)

### 3.6 Bug Fixes
- [x] SQLite datetime format: modernc sqlite stores as ISO 8601 (`T`/`Z`), added `ParseDBTime()` multi-format parser
- [x] OUI database: removed 9 duplicate MAC prefix keys
- [x] `MarkStaleHostsOffline` now returns which hosts went offline (for notifications)

### 3.7 Verification
- [x] ARP scan discovers 10 devices on local network (FRITZ!Box router, repeater, laptops, Amazon device, etc.)
- [x] OUI vendor lookup works (Microsoft Hyper-V identified from MAC)
- [x] Dashboard cleanly separates 2 agent nodes from 10 passive devices
- [x] Devices page shows all passive devices with details
- [x] Settings page shows ntfy sender active, thresholds at 90%
- [x] "Send Test" button sends notification to ntfy.sh (verified via API poll)
- [x] Docker containers discovered on both Windows (3 containers) and Ubuntu (13 containers)
- [x] Deployed to both Main16GB (Windows) and ubuntu (Linux) nodes

---

## Phase 4: Intelligence

**Goal:** LLM chat interface for natural language CMDB queries.

### 4.1 Ollama LLM Integration
- [x] `internal/hub/llm/client.go` -- Ollama API client (non-streaming, /api/chat + /api/tags)
- [x] `internal/hub/llm/tools.go` -- 5 tool functions (list_hosts, get_host, get_metrics, list_services, get_summary)
- [x] `internal/hub/llm/chat.go` -- conversation handler with tool-calling loop (max 5 rounds)
- [x] Tool executor: queries Store directly for real-time CMDB data
- [x] Session management: per-session conversation history with 20-message trim
- [x] `--llm <url>` flag activates LLM (e.g., `--llm http://localhost:11434`)
- [x] `--llm-model <name>` flag selects model (default: `qwen2.5:7b`)
- [x] Graceful fallback: warns and disables chat if Ollama is unreachable

### 4.2 Chat UI
- [x] `web/templates/chat.html` -- full chat page with Alpine.js reactive app
- [x] Suggestion buttons: "What hosts are online?", "Show Docker containers", "Any disk issues?", "Homelab summary"
- [x] Ollama connection status indicator (green/red dot)
- [x] Model badge display
- [x] Clear conversation button
- [x] Auto-scroll, loading spinner, basic markdown formatting (bold, code)
- [x] Nav bar: Chat link with brain icon

### 4.3 LLM API Endpoints
- [x] `GET /api/v1/llm/status` -- connection check + model info
- [x] `POST /api/v1/llm/chat` -- send message, get response (handles tool-calling internally)
- [x] `POST /api/v1/llm/clear` -- clear session history

### 4.4 Tool Functions
- [x] `list_hosts` -- filter by status (online/offline/all) and monitor_type (agent/passive/all)
- [x] `get_host` -- case-insensitive partial hostname match, returns full host details
- [x] `get_metrics` -- latest snapshot or history summary (avg/max CPU/mem over N hours)
- [x] `list_services` -- filter by hostname and/or category, excludes unknown ports
- [x] `get_summary` -- fleet overview: host counts, online/offline, service counts by category

### 4.5 Verification
- [x] Ollama connected with qwen2.5:7b model
- [x] Tool-calling works: LLM calls list_hosts, get_summary, etc. and synthesizes answers
- [x] Chat UI renders cleanly, suggestion buttons trigger queries
- [x] "Give me a homelab summary" returns accurate data (12 hosts, 2 agents, 10 passive, 5 services)
- [x] Conversation context maintained across follow-up questions

### 4.6 Remaining (Future)
- [ ] Anomaly detection: baseline learning for CPU/mem/disk patterns
- [ ] Dashboard: alert history feed

---

## Phase 4b: Charts & Device Management

**Goal:** Inline metric history charts and device management UI.

### 4b.1 Chart.js Metric Charts
- [x] `GET /api/v1/hosts/{id}/history?hours=N` -- chart-ready JSON endpoint with downsampling (~200 points)
- [x] Host detail page: 4 time-series charts (CPU, Memory, Load Average, Network I/O)
- [x] Selectable time ranges: 1h, 6h, 24h, 3d, 7d
- [x] Dashboard cards: CPU sparkline (last 1 hour) on each agent card
- [x] Chart.js globals: dark theme, compact sizing (100px height)
- [x] Network chart: delta calculation between consecutive points (sent/recv)

### 4b.2 Device Management
- [x] Device category dropdown (select device_type per device: server, desktop, phone, IoT, etc.)
- [x] `POST /api/v1/hosts/{id}/type` -- update device type
- [x] Delete host/device with cascade (metrics, services, alerts)
- [x] `DELETE /api/v1/hosts/{id}` -- delete endpoint with confirmation
- [x] Delete button on host detail page (redirects to dashboard)
- [x] `display_name` column (migration 003) for user-defined names
- [x] Inline rename via pen icon (Alpine.js)
- [x] `hostLabel()` template function (display_name > hostname)
- [x] DNS suffix stripping (`.fritz.box`, `.local`, `.lan`, `.home`, `.localdomain`, `.internal`)
- [x] OUI check button on devices page (htmx POST to `/api/v1/oui-check`)
- [x] Online OUI fallback via macvendors.com API with in-memory caching

---

## Phase 5: Integrations

**Goal:** Pull data from external homelab controllers via secure credential store.

### 5.1 Secure Credential Store
- [x] AES-256-GCM encrypted secrets in SQLite (`secrets` table)
- [x] Local keyfile (`secret.key`) per node, 32 bytes from crypto/rand
- [x] `internal/store/secrets.go` -- SetSecret, GetSecret, DeleteSecret
- [x] No passwords in CLI flags or environment variables

### 5.2 Integration Framework
- [x] `integrations` table (migration 004) -- type, name, config JSON, status, last_sync
- [x] `internal/store/integrations.go` -- CRUD for integration configs
- [x] Settings UI: add/edit/delete integrations, test connectivity, manual sync
- [x] `POST /api/v1/integrations` -- save integration (credentials encrypted)
- [x] `DELETE /api/v1/integrations/{id}` -- remove integration + secrets
- [x] `POST /api/v1/integrations/{id}/test` -- test connectivity
- [x] `POST /api/v1/integrations/{id}/sync` -- manual device sync
- [x] Background auto-sync every 5 minutes for all enabled integrations

### 5.3 FRITZ!Box Integration (TR-064)
- [x] `internal/agent/integrations/fritzbox.go` -- TR-064 SOAP client
- [x] HTTP Digest Authentication (RFC 2617) implementation
- [x] `GetHostNumberOfEntries` + `GetGenericHostEntry` iteration
- [x] Returns hostname, IP, MAC, active status per device
- [x] OUI vendor lookup + device classification on synced devices
- [x] Deduplication with existing ARP/mDNS devices via MAC-based deterministic IDs
- [x] Verified: 50 devices synced from FRITZ!Box 7590 AX (67 known, 50 with valid IPs)

### 5.4 Verification
- [x] FRITZ!Box TR-064 authenticated via Settings UI (Test button: "Connected!")
- [x] Sync pulled 50 devices including Tuya IoT, Sonos, Samsung Washer, Philips Hue, iPhones, Galaxy Tab
- [x] Devices deduplicated with ARP-discovered entries (same MAC = same device ID)
- [x] Dashboard shows 53 total hosts (2 agents + 51 passive devices)
- [x] Integration status persisted (green checkmark on Settings page)

### 5.5 Additional Integrations
- [x] `internal/agent/integrations/unifi.go` -- Unifi controller REST API (login + fetch clients)
- [x] `internal/agent/integrations/homeassistant.go` -- HA REST API (device_tracker entities)
- [x] `internal/agent/integrations/pihole.go` -- Pi-hole admin API (network clients)
- [x] `internal/agent/integrations/pfsense.go` -- pfSense REST API (ARP table)
- [x] All integration types wired into Settings UI (test/sync/auto-sync)
- [x] pfSense added to Settings UI dropdown

### 5.6 SNMP Scanner
- [x] `internal/agent/scanners/snmp.go` -- Pure Go SNMPv2c (no external dependency)
- [x] Raw ASN.1/BER packet construction and parsing
- [x] Queries sysName (1.3.6.1.2.1.1.5.0) and sysDescr (1.3.6.1.2.1.1.1.0)
- [x] CIDR expansion for target ranges (max /24)
- [x] Device classification from sysDescr keywords (router, switch, printer, NAS, etc.)

### 5.7 External Plugin System
- [x] `internal/plugin/external.go` -- subprocess-based plugin framework
- [x] JSON protocol on stdin/stdout (request/response per line)
- [x] Methods: name, type, detect, interval, collect (observer), scan (scanner)
- [x] Auto-load from `<data-dir>/plugins/` directory at startup
- [x] Supports observer and scanner plugin types

---

## Phase 6: Security & Polish

**Goal:** Production-ready for homelab deployment.

### 6.1 Web UI Authentication
- [x] `internal/hub/api/auth.go` -- AuthManager with token-based auth
- [x] Auto-generated 24-char hex token stored in `~/.homelabmon/auth-token`
- [x] HMAC-SHA256 signed session cookies (`hlm_session`), 7-day expiry
- [x] Auth middleware wraps all UI/API routes
- [x] Mesh peer routes exempted from auth (`/api/v1/register`, `/api/v1/heartbeat`, etc.)
- [x] `web/templates/login.html` -- standalone login page (dark theme)
- [x] Login/logout handlers + logout button in nav bar
- [x] `--no-auth` flag to disable auth on trusted networks
- [x] Verified: redirect to login, wrong token rejected, correct token sets cookie, session works, logout clears cookie

### 6.2 Configurable Settings via UI
- [x] Migration 005: `settings` table (key-value store)
- [x] `internal/store/settings.go` -- GetSetting, SetSetting, AllSettings
- [x] `POST /api/v1/settings` -- save settings (persists to DB, updates viper at runtime)
- [x] Settings page: editable CPU/memory/disk thresholds and metric retention days
- [x] Settings page: ntfy.sh and webhook notification URLs (no CLI required)
- [x] Notification senders rebuilt at runtime when URLs change (Dispatcher.SetSenders)
- [x] Test button sends test notification (disabled when no URL configured)
- [x] `--retention-days` CLI flag (default 7, 0 = forever)
- [x] DB settings loaded at startup (CLI flags override DB values)
- [x] Purge goroutine reads retention dynamically from viper each cycle
- [x] Node Info card shows auth + scanning status
- [x] Verified: save button works, values persist across page reloads and restarts

### 6.3 Install & Service Management
- [x] `install.sh` -- one-liner installer: detects OS/arch, downloads binary, installs to /usr/local/bin
- [x] `homelabmon setup --systemd` -- generates and installs systemd service unit (Linux)
- [x] `homelabmon setup --launchd` -- generates and installs launchd plist (macOS)
- [x] `homelabmon setup --windows-service` -- installs as Windows service via sc.exe (auto-start, restart on failure)
- [x] `--uninstall` flag for all three: clean removal of service
- [x] Auto-detects current user, binary path; helpful output with status/logs/restart commands
- [x] Clear "run as Administrator" error on Windows when privileges are missing

### 6.4 UI Polish
- [x] Help tooltips on all settings (CSS `::after` overlay on hover)
- [x] Descriptions: ntfy, webhook, CPU/mem/disk thresholds, metric retention

### 6.5 mTLS & Enrollment
- [x] `internal/mesh/pki.go` -- ECDSA P-256 CA generation, node cert signing, CSR handling
- [x] `homelabmon setup --gen-ca` -- creates CA + node cert (10-year CA, 5-year node)
- [x] `homelabmon setup --gen-token` -- generates one-time enrollment token (stored in DB)
- [x] `POST /api/v1/enroll` -- enrollment endpoint: validates token, signs CSR, returns cert + CA
- [x] Token invalidated after single use (security: prevents replay)
- [x] Transport auto-detects certs: uses `ListenAndServeTLS` when `ca.crt` + `node.crt` exist
- [x] `ClientAuth = VerifyClientCertIfGiven` -- browsers work without client certs, peers use mTLS
- [x] Heartbeat client uses mTLS when certs are loaded (`SetTLSConfig`)
- [x] `--enroll-url` + `--enroll-token` flags for new node enrollment (generates key, sends CSR, saves certs)
- [x] Enrollment uses `InsecureSkipVerify` only for initial connection (don't have CA cert yet)
- [x] TLS 1.3 minimum
- [x] Verified: CA generation, token generation, HTTPS serving, browser access, enrollment rejection

### 6.6 Config Hot-Reload
- [x] `viper.WatchConfig()` watches `~/.homelabmon/config.yaml` for changes
- [x] `OnConfigChange` callback updates: log level, notification senders, thresholds, retention
- [x] Threshold check goroutine reads thresholds dynamically from viper each cycle (no restart needed)
- [x] Settings changed via web UI also apply instantly via viper

### 6.7 Peer Federation (Multi-Site)
- [x] Migration 006: `site` column on `hosts` and `peers` tables
- [x] `--site` CLI flag and config option for labeling nodes
- [x] Site label configurable from Settings UI (saved to DB + viper)
- [x] Gossip protocol: heartbeats include `KnownPeers` list (ID + address + site)
- [x] Auto-discovery: nodes add unknown peers from gossip (transitive peer discovery)
- [x] Both heartbeat sender and handler process gossip peers
- [x] Dashboard groups hosts by site when multiple sites exist (`HasMultiSites`)
- [x] Host card extracted to reusable `host-card` template block
- [x] Node Info card on Settings page shows current site label
- [x] README updated with federation and hot-reload documentation
- [x] Verified: two-node mesh with `--site home`, gossip peer discovery, site grouping

---

## Phase 7: LLM Management Agent

**Goal:** Turn the read-only LLM chat into a full homelab agent with access to all platform functions, able to manage connected systems across the mesh.

- [x] `internal/mesh/client.go` -- PeerClient: routes management calls to the owning node (loopback for local, peer address + mTLS client cert for remote); generic `PostJSON` for future actions
- [x] `internal/hub/llm/tools.go` -- ToolExecutor expanded 5 -> 19 tools: 9 read (hosts/metrics/services/containers/peers/integrations/settings) + 10 management (docker_control, trigger_network_scan, send_notification, update_settings, rename_host, set_device_type, delete_host, manage_integration, check_vendors, add_peer)
- [x] Container resolution by name/image/ID with cross-host ambiguity detection
- [x] Confirmation gates enforced executor-side for stop/restart/delete actions (model-independent)
- [x] `chat.go` -- agent system prompt with safety rules, tool rounds 5 -> 8, per-response action log
- [x] `api/handleDockerControl` refactored onto PeerClient (also fixes remote control on mTLS meshes, which previously used plain HTTP)
- [x] Chat UI: "AI Agent" panel, action badges under assistant messages, management suggestion buttons
- [x] Unit tests: confirm gating, routing, settings persistence, host CRUD, notification sender checks, tool dispatch completeness (`TestAllToolDefinitionsExecutable`)
- [x] DESIGN.md/README updated with the full tool catalogue

---

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-03-05 | Single Go binary, no master | Simplicity, zero SPOF, any node = dashboard |
| 2026-03-05 | SQLite only (no Redis) | Single process, no external deps, handles homelab scale |
| 2026-03-05 | modernc.org/sqlite (pure Go) | No CGO = trivial cross-compilation to all 6 targets |
| 2026-03-05 | Plugin architecture (4 types) | Future-proof: observers now, probes/scanners/integrations later |
| 2026-03-05 | Unified hosts table | Agent nodes, passive devices, and API-discovered devices in one CMDB |
| 2026-03-05 | htmx + Alpine.js (no React/Next) | No build step, embedded in binary, works offline |
| 2026-03-05 | Ollama for LLM | Local, self-hosted, supports tool-calling, no cloud dependency |
| 2026-03-05 | Port 9600 default | Avoids conflicts with common homelab services |
| 2026-03-05 | Separate template parsing per page | Avoids Go template `define` name collisions |
| 2026-03-05 | PersistentFlags for run flags | Ensures viper sees flags on both root and subcommands |
| 2026-03-05 | Docker API via raw HTTP (no SDK) | Zero extra deps, works on both unix socket and Windows named pipe |
| 2026-03-05 | Embedded fingerprints.yaml | Single binary, no external config files required |
| 2026-03-05 | Filter unknown ports from services UI | Only show fingerprint/docker matches, reduce noise |
| 2026-03-05 | go-winio for Windows named pipes | Go net.Dial("unix") doesn't work for Windows pipes |
| 2026-03-05 | Try both Docker pipe paths on Windows | Docker Desktop uses `dockerDesktopLinuxEngine`, traditional uses `docker_engine` |
| 2026-03-05 | Embedded OUI database (not downloaded) | Single binary, no network required, 800+ prefixes covers common devices |
| 2026-03-05 | ntfy.sh as primary notification | Free, self-hostable, mobile app, zero config, simple HTTP POST |
| 2026-03-05 | 10-minute notification cooldown | Prevent alert storms; same host/category/severity suppressed |
| 2026-03-05 | Separate agent nodes from passive devices on dashboard | Agents have metrics (large cards), passives have presence only (compact grid) |
| 2026-03-05 | ParseDBTime multi-format | modernc sqlite stores ISO 8601 (T/Z) vs standard sqlite space-separated |
| 2026-03-05 | Non-streaming Ollama API | Simpler implementation, tool-calling requires full response before processing |
| 2026-03-05 | qwen2.5:7b as default model | Good tool-calling support, runs on 16GB RAM, fast inference |
| 2026-03-05 | Per-session conversation history | Allows multiple browser tabs, 20-message trim prevents context overflow |
| 2026-03-05 | 5 tool functions (not raw SQL) | Structured queries are safer, LLM can't corrupt data or run arbitrary SQL |
| 2026-03-05 | Alpine.js for chat reactivity | Already in stack, no new deps, handles async fetch + DOM updates cleanly |
| 2026-03-05 | Chart.js for metric history | Already loaded via CDN, category-based x-axis (no date adapter needed), downsampled to ~200 points |
| 2026-03-05 | 100px chart height | User feedback: default Chart.js charts were "far too big", compact 100px fits 2x2 grid well |
| 2026-03-05 | AES-256-GCM for integration secrets | Industry standard AEAD, keyfile per node, no passwords in CLI/env |
| 2026-03-05 | HTTP Digest Auth for FRITZ!Box | TR-064 requires digest auth, implemented RFC 2617 natively (no external dep) |
| 2026-03-05 | MAC-based dedup for integrations | UpsertPassiveDevice uses SHA256(MAC) as deterministic ID, so FRITZ!Box and ARP entries merge automatically |
| 2026-03-05 | Settings UI for integration config | User requested "secure store, not env or command line" -- web UI with encrypted storage is the right UX |
| 2026-03-05 | Pure Go SNMPv2c (no gosnmp dep) | Raw ASN.1/BER packet build/parse, zero external deps, keeps single-binary promise |
| 2026-03-05 | External plugin via JSON stdin/stdout | Simple, language-agnostic, subprocess isolation, no shared memory or RPC complexity |
| 2026-03-05 | InsecureSkipVerify for Unifi/pfSense | Self-signed certs are common in homelab; controllers typically run on LAN only |
| 2026-03-05 | HA token stored as "password" in secrets | Unified credential handling -- all integrations use same SetSecret/GetSecret pattern |
| 2026-03-05 | Token-based web auth (not basic auth) | Random hex token in file, HMAC-signed cookies, no username/password to manage |
| 2026-03-05 | Mesh routes exempt from auth | Peers need /api/v1/heartbeat and /api/v1/register without browser sessions |
| 2026-08-24 | PeerClient for routed management | One place resolves local-vs-peer targets; docker control gains mTLS support and is reusable by both UI and LLM agent |
| 2026-08-24 | Confirmation gates enforced in executor | The model is instructed to ask first, but the executor refuses stop/restart/delete without confirm=true regardless of what the model attempts |
| 2026-08-24 | 19 structured tools (still no raw SQL) | Agent covers the full platform surface; destructive ops gated; credentials never enter chat history (integration passwords only via Settings UI) |
| 2026-08-24 | Action log in chat responses | Users see which tools the agent executed as badges; also written to zerolog |

---

## Deployment Notes

| Node | OS | Address | Binary | Service |
|------|----|---------|--------|---------|
| Main16GB | Windows 11 Pro 25H2 | 192.168.178.44:9600 | Run from source (`go run .`) | Manual |
| ubuntu | Ubuntu 24.04 | 192.168.178.45:9600 | /opt/homelabmon/homelabmon | systemd (homelabmon.service, user=dx) |
