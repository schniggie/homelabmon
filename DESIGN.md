# HomelabMon (homelabmon)

A single-binary, zero-dependency homelab discovery and monitoring system with mesh networking, auto-discovery, lightweight CMDB, and local LLM integration.

## Why This Exists

No existing open-source tool combines:
- Single binary, zero external deps (agent side)
- Auto-discovery with homelab service fingerprinting (Pi-hole, Plex, Home Assistant, Jellyfin...)
- Local LLM chat interface (Ollama) -- "What's running on my NAS?"
- Pure mesh (no master, any node = dashboard)
- Lightweight CMDB built from discovered data
- Cross-platform single binary (Win/Mac/Linux/RPi)

Closest competitor is [Beszel](https://github.com/henrygd/beszel) (Go agent+hub) but it lacks discovery, LLM, and mesh.

## Architecture

```
                  homelabmon binary (same on every node)
         ┌─────────────────────────────────────────────┐
         │  Plugin Registry                            │
         │  ┌──────────┐ ┌───────┐ ┌────────┐ ┌─────┐ │
         │  │ Observers │ │Probes │ │Scanners│ │Integ│ │
         │  │ CPU,mem,  │ │Docker,│ │ARP,    │ │Unifi│ │
         │  │ disk,net  │ │DB,web │ │mDNS,   │ │HA,  │ │
         │  │           │ │       │ │SNMP    │ │pfS  │ │
         │  └─────┬─────┘ └───┬───┘ └───┬────┘ └──┬──┘ │
         │        │           │         │          │    │
         │  ┌─────┴───────────┴─────────┴──────────┴──┐ │
         │  │              Collector                    │ │
         │  └─────────────────┬────────────────────────┘ │
         │                    │                          │
         │  ┌─────────────────┴────────────────────────┐ │
         │  │           SQLite Store                    │ │
         │  │  hosts | metrics | services | alerts      │ │
         │  └─────────────────┬────────────────────────┘ │
         │                    │                          │
         │  ┌────────┐  ┌────┴─────┐  ┌──────────────┐  │
         │  │ Web UI │  │ Mesh API │  │ LLM Bridge   │  │
         │  │ (--ui) │  │ (peers)  │  │ (--llm)      │  │
         │  └────────┘  └──────────┘  └──────────────┘  │
         └─────────────────────────────────────────────┘

    Node A ◄──────────────► Node B ◄──────────────► Node C
    (--ui --llm)            (bare)                   (--ui --scan)
    RPi 4                   NAS                      Mac Mini
```

## Single Binary, Three Roles

Every node runs the same binary. Capabilities are activated via flags:

```bash
homelabmon                                    # bare node: observe + peer
homelabmon --ui                               # + web dashboard
homelabmon --llm http://localhost:11434       # + LLM chat (Ollama, default model: qwen2.5:7b)
homelabmon --llm-model mistral:7b            # + use different Ollama model
homelabmon --scan                             # + network scanning (ARP, mDNS)
homelabmon --notify-ntfy https://ntfy.sh/x   # + push notifications
homelabmon --ui --scan --llm ... --notify-ntfy ...  # full features
```

No master. No hub. Any node can do anything.

## Three Monitoring Layers

| Layer | Devices | Method | Data |
|-------|---------|--------|------|
| **Agent** | Servers, PCs, NAS, RPi | `homelabmon` binary runs on host | Full metrics, services, deep monitoring |
| **Passive** | Phones, tablets, TVs, IoT, printers | ARP/mDNS/ping from `--scan` node | Presence, MAC, vendor, device type |
| **Integration** | FRITZ!Box, Unifi, Home Assistant, pfSense | API pull from controller | Clients, config, status |

## Plugin System

Four plugin types, one registry:

```
Plugin (base)
├── Observer    local system metrics        [CPU, memory, disk, network, processes]
├── Probe       service-specific monitoring  [Docker, PostgreSQL, Redis, nginx, certs]
├── Scanner     passive network discovery    [ARP, mDNS, SNMP, ping sweep]
└── Integration external API data pull       [Unifi, Home Assistant, pfSense, Pi-hole]
```

Plugins auto-detect their environment. Docker probe only starts if Docker daemon is found. ARP scanner only runs if `--scan` is enabled. Unifi integration only activates if configured.

## Technology Stack

| Component | Technology | Why |
|-----------|-----------|-----|
| Language | Go 1.23+ | Single binary, cross-compile, low memory, system-level access |
| Database | SQLite (modernc.org/sqlite) | Pure Go, no CGO, embedded, zero config |
| CLI | cobra + viper | Industry standard, flags + env + config file |
| System metrics | gopsutil/v4 | Cross-platform (Win/Mac/Linux), pure Go |
| LAN discovery | hashicorp/mdns | Peer auto-discovery on LAN |
| Web UI | Go templates + htmx + Alpine.js + Chart.js | No build step, embedded in binary |
| Icons | Font Awesome | Per user preference |
| CSS | Tailwind (CDN) | Utility-first, no build step |
| LLM | Ollama API | Local, self-hosted, tool-calling |

## CMDB Model

Unified `hosts` table for all device types:

```
hosts
├── id, hostname, display_name, status (online/offline)
├── monitor_type: agent | passive | integration
├── device_type: server/desktop/laptop/phone/tablet/tv/media/iot/printer/camera/router/switch/ap/nas
├── discovered_via: agent/arp/mdns/fritzbox/unifi/homeassistant/pihole
├── os, arch, platform, kernel (agent nodes)
├── cpu_model, cpu_cores, memory_total (agent nodes)
├── ip_addresses, mac_address, vendor (all)
└── first_seen, last_seen
```

## Peer Protocol

Nodes communicate over HTTP JSON API on port 9600:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/register` | POST | Register with peer |
| `/api/v1/heartbeat` | POST | Exchange metrics (bidirectional) |
| `/api/v1/status` | GET | Node status |
| `/api/v1/peers` | GET | Known peers |
| `/api/v1/hosts` | GET | All known hosts |
| `/api/v1/metrics/latest` | GET | Latest snapshot |
| `/api/v1/metrics/history` | GET | Time-series data |
| `/api/v1/exec` | POST | Run a command on this node (only with `--exec`) |
| `/api/v1/docker/control` | POST | Start/stop/restart a container |

Heartbeat every 60s. Bidirectional -- response includes peer's own heartbeat. Failed heartbeats mark peer offline.

## LLM Integration (Phase 4)

Local Ollama with tool-calling. Activated via `--llm http://localhost:11434 --llm-model qwen2.5:7b`.

The LLM is a full homelab agent: it can query the CMDB **and manage the platform**.
Tool calls are routed to the node that owns the target (loopback for the local
node, peer address over mTLS for remote nodes) via `mesh.PeerClient`.

Read tools:
```
list_hosts(status?, monitor_type?)     -> all hosts, filterable by status and type
get_host(hostname)                     -> details (case-insensitive partial match)
get_metrics(hostname, hours?)          -> latest snapshot or history summary (avg/max CPU/mem)
list_services(hostname?, category?)    -> discovered services, filterable
get_summary()                          -> fleet overview: counts, online/offline, services by type
list_docker_containers(hostname?)      -> containers with image, stack, health, ports, container ID
list_peers()                           -> mesh peers: address, status, version, site
list_integrations()                    -> FRITZ!Box/Unifi/HA/Pi-hole/pfSense with sync status
get_settings()                         -> thresholds, retention, scan interval, notification channels
```

Management tools:
```
run_command(command, hostname?, shell?, timeout_seconds?, confirm) -> run on any agent node: Linux/macOS/BSD via sh, Windows via cmd/powershell; audit-trailed
list_exec_history(hostname?, limit?)                       -> recent command executions
recall_memory(hostname?, kind?, limit?)                    -> past actions and notes for a node (or global), newest first
remember(title, detail?, hostname?)                        -> store a note for future sessions
docker_control(container, hostname?, action, confirm?)     -> start/stop/restart containers on any node
trigger_network_scan()                                     -> run ARP+mDNS scan now, returns device count
send_notification(title, message, severity?)               -> push via ntfy/webhook
update_settings(...)                                       -> thresholds, retention, scan interval, URLs, site
rename_host(hostname, new_name)                            -> set display name
set_device_type(hostname, device_type)                     -> fix classification
delete_host(hostname, confirm)                             -> remove from CMDB
manage_integration(action, name, confirm?)                 -> test / sync / delete integrations
check_vendors()                                            -> re-resolve MAC OUI vendors
add_peer(address)                                          -> connect a new node to the mesh
```

Remote execution (`--exec` per node, off by default): commands run in the node's
native shell (`sh` on Unix incl. FreeBSD/OPNsense, `cmd.exe`/PowerShell on
Windows), in their own process group so timeouts kill the whole tree; output is
capped at 32 KB per stream, timeouts up to 600 s. Every execution is recorded
in `exec_history` on the requesting node. The tool requires `confirm=true` for
every single command.

Safety: disruptive actions (stop/restart container, delete host, delete integration)
carry a `confirm` parameter. The system prompt instructs the model to ask the user
first and only pass `confirm=true` after explicit agreement; the executor enforces
the gate independently of the model. Executed actions are shown as badges in the
chat UI and logged.

## Agent Memory (Phase 9)

Two layers of persistence so the agent improves with use:

**Node memory** (`memories` table): every successful management tool call
(docker_control, run_command, update_settings, ...) is automatically recorded
with a per-tool summary title, scoped to the affected node (`node:<id>`) or
global. The agent can also store free-form notes (`remember`) -- fixes applied
and why, config locations, known issues, user preferences -- and recalls
everything with `recall_memory` when working on that node again. Recording
never fails the action it describes.

**Chat sessions** (`chat_sessions`/`chat_messages`): conversations persist
across restarts. Each session gets an LLM-generated title (async, after the
first exchange; falls back to a truncated user message). Messages carry the
tool-action log so the UI replays what was executed. Endpoints:
`GET /api/v1/llm/sessions`, `GET .../sessions/{id}/messages`,
`DELETE .../sessions/{id}`. The sidebar's History panel lists, loads, and
deletes sessions.

Chat UI in the sidebar with suggestion buttons, session history, and an action
trail. Tool-calling loop (max 32 rounds) handles multi-step and fleet-wide workflows automatically.

Users chat naturally: "What's running on the NAS?", "Any disk issues?", "Show me
all Docker containers", "Restart jellyfin on the NAS", "Scan the network now".

## Notifications

Push notifications for host status changes and resource threshold alerts:

```bash
homelabmon --notify-ntfy https://ntfy.sh/my-topic          # ntfy.sh (mobile push)
homelabmon --notify-webhook https://discord.com/api/...     # Discord/Slack/custom
homelabmon --notify-cpu-threshold 90                         # alert when CPU > 90%
homelabmon --notify-mem-threshold 90                         # alert when memory > 90%
homelabmon --notify-disk-threshold 90                        # alert when disk > 90%
```

Supported events:
- Host goes offline (automatic detection via stale heartbeat)
- CPU/memory/disk exceeds threshold
- Test notification via Settings UI

10-minute cooldown dedup prevents alert storms.

## Integrations (Phase 5)

External API integrations configured via Settings UI. Credentials encrypted with AES-256-GCM (local keyfile per node).

| Integration | Protocol | Status |
|------------|----------|--------|
| **FRITZ!Box** | TR-064 SOAP + HTTP Digest Auth | Implemented |
| **Unifi** | REST API (login + cookie session) | Implemented |
| **Home Assistant** | REST API (Bearer token) | Implemented |
| **Pi-hole** | Admin API (API key auth) | Implemented |
| **pfSense** | REST API (Basic auth) | Implemented |

Integrations auto-sync every 5 minutes. Devices deduplicated with existing ARP/mDNS entries by MAC address.

## Metric Charts

Host detail page includes Chart.js time-series for CPU, Memory, Load Average, and Network I/O. Time range selectable (1h/6h/24h/3d/7d). Dashboard cards include CPU sparklines. Data downsampled to ~200 points via `GET /api/v1/hosts/{id}/history`.

## Security (Phase 6)

- `homelabmon setup` generates CA
- Agent enrollment via one-time token
- mTLS after enrollment
- Web UI auth via token
- LLM runs locally only

## Build Targets

```
linux/amd64    linux/arm64    linux/arm (RPi3/Zero)
darwin/amd64   darwin/arm64   (Apple Silicon)
windows/amd64
```

## Directory Structure

```
HomelabMon/
├── main.go
├── go.mod / go.sum
├── Makefile
├── DESIGN.md                    # this file
├── PROGRESS.md                  # phase tracker
├── cmd/
│   ├── root.go                  # cobra root + config
│   ├── run.go                   # main command
│   ├── setup.go                 # setup wizard
│   └── version.go               # version info
├── internal/
│   ├── models/                  # data structs
│   ├── plugin/                  # plugin interfaces + registry
│   ├── store/                   # SQLite store + migrations
│   ├── agent/                   # collector + observers + heartbeat
│   │   ├── observers/           # CPU, mem, disk, net, ports, processes, docker
│   │   ├── discovery/           # fingerprint engine + service matching
│   │   ├── scanners/            # ARP, mDNS, SNMP, OUI lookup
│   │   └── integrations/       # FRITZ!Box, Unifi, Home Assistant, Pi-hole, pfSense
│   ├── notify/                  # notification senders (ntfy.sh, webhook)
│   ├── mesh/                    # peer transport + API handlers
│   └── hub/                     # web UI server
│       ├── api/                 # UI HTTP server + page handlers
│       └── llm/                 # Ollama client, tool executor, chat handler
├── web/                         # embedded templates + static
│   ├── embed.go
│   ├── templates/               # layout, dashboard, host, services, devices, settings, chat
│   └── static/
└── configs/                     # fingerprints.yaml (embedded)
```
