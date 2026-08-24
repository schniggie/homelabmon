# HomelabMon

A single-binary, zero-dependency homelab discovery and monitoring system with mesh networking, auto-discovery, lightweight CMDB, and local LLM integration.

<p align="center">
  <img src="docs/screenshot-dashboard.jpg" alt="HomelabMon Dashboard" width="100%">
</p>

<p align="center">
  <em>Three-node mesh: Windows PC, QNAP NAS, Ubuntu server -- with network device discovery, Docker containers, and AI chat</em>
</p>

## Features

- **Single binary** -- one Go binary, runs on Linux/macOS/Windows/RPi
- **Pure mesh** -- no master node, any node can serve the dashboard
- **Auto-discovery** -- fingerprints 60+ homelab services (Pi-hole, Plex, Jellyfin, Home Assistant, etc.)
- **Network scanning** -- ARP, mDNS, SNMP discover passive devices (phones, TVs, IoT, printers)
- **Integrations** -- FRITZ!Box, Unifi, Home Assistant, Pi-hole, pfSense API pulls
- **LLM chat** -- local Ollama integration: "What's running on my NAS?"
- **AI agent** -- the chat can also *manage* the homelab: start/stop/restart Docker containers on any node, trigger scans, send notifications, change settings, rename/remove devices, test/sync integrations, and add mesh peers (with confirmation gates on destructive actions)
- **Lightweight CMDB** -- all devices in one SQLite database
- **Notifications** -- ntfy.sh + webhook alerts for host offline / resource thresholds
- **Secure credentials** -- AES-256-GCM encrypted secret store, no passwords in CLI or env
- **External plugins** -- extend with any language via subprocess JSON protocol

## Install

Download the latest binary for your platform from [Releases](https://github.com/dx111ge/homelabmon/releases/latest). No dependencies, no compilation needed.

### Quick Install (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/dx111ge/homelabmon/main/install.sh | sh
```

This auto-detects your OS and architecture, downloads the right binary, and installs it to `/usr/local/bin`.

### Linux (x86_64)

```bash
curl -sL https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-linux-amd64 -o homelabmon
chmod +x homelabmon
sudo mv homelabmon /usr/local/bin/
homelabmon --ui
```

### Linux ARM64 (RPi 4/5, Oracle Cloud)

```bash
curl -sL https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-linux-arm64 -o homelabmon
chmod +x homelabmon
sudo mv homelabmon /usr/local/bin/
homelabmon --ui
```

### Linux ARM (RPi 3/Zero)

```bash
curl -sL https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-linux-arm -o homelabmon
chmod +x homelabmon
sudo mv homelabmon /usr/local/bin/
homelabmon --ui
```

### macOS (Apple Silicon)

```bash
curl -sL https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-darwin-arm64 -o homelabmon
chmod +x homelabmon
sudo mv homelabmon /usr/local/bin/
homelabmon --ui
```

### macOS (Intel)

```bash
curl -sL https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-darwin-amd64 -o homelabmon
chmod +x homelabmon
sudo mv homelabmon /usr/local/bin/
homelabmon --ui
```

To run on login as a launchd service:

```bash
homelabmon setup --launchd
```

### Windows

Download [homelabmon-windows-amd64.exe](https://github.com/dx111ge/homelabmon/releases/latest/download/homelabmon-windows-amd64.exe) and run:

```powershell
.\homelabmon-windows-amd64.exe --ui
```

To install as a Windows service (run as Administrator):

```powershell
.\homelabmon-windows-amd64.exe setup --windows-service
```

### Build from Source

```bash
go build -o homelabmon .
./homelabmon --ui
```

Then open **http://localhost:9600**

## Linux Permissions

HomelabMon runs as a regular user for basic metrics. Some features require group membership or capabilities:

| Feature | Requirement | Why |
|---------|------------|-----|
| Basic metrics (CPU, mem, disk) | None | Reads from `/proc`, no special access needed |
| Docker containers | `docker` group | Reads `/var/run/docker.sock` |
| Network scanning (`--scan`) | `root` or `cap_net_raw` | ARP/mDNS use raw sockets for discovery |
| Listening ports | `root` or same user | `gopsutil` reads `/proc/net/tcp` -- sees all ports as root, only own ports as user |
| Bind to port < 1024 | `root` or `cap_net_bind_service` | Default port 9600 does not need this |

### Recommended Setup

Add your user to the `docker` group (if you want Docker discovery):

```bash
sudo usermod -aG docker $USER
# Log out and back in for the group change to take effect
```

For network scanning without running as root, grant the binary the necessary capability:

```bash
sudo setcap cap_net_raw+ep /usr/local/bin/homelabmon
```

### Running as a systemd Service

The easiest way to set up a systemd service:

```bash
homelabmon setup --systemd
```

This generates the unit file, enables, and starts the service automatically. Or create it manually:

```ini
# /etc/systemd/system/homelabmon.service
[Unit]
Description=HomelabMon monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=YOUR_USER
Group=YOUR_USER
ExecStart=/usr/local/bin/homelabmon --ui --scan
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_RAW
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now homelabmon
```

The `AmbientCapabilities=CAP_NET_RAW` line grants network scanning without running as root. The service user should be in the `docker` group if Docker discovery is wanted.

## Authentication

The web UI is protected by a token-based login. On first launch, a random access token is generated and saved to `~/.homelabmon/auth-token`. You'll need this token to sign in.

```bash
# View your token
cat ~/.homelabmon/auth-token
```

On Windows the file is at `%USERPROFILE%\.homelabmon\auth-token`.

When you open the dashboard, you'll be redirected to a login page. Paste the token to sign in. Sessions last 7 days.

Mesh peer routes (`/api/v1/heartbeat`, `/api/v1/register`, etc.) are **not** behind auth, so node-to-node communication works without tokens.

To disable auth on a trusted network:

```bash
homelabmon --ui --no-auth
```

## Mesh Security (mTLS)

For encrypted and authenticated peer-to-peer communication:

```bash
# On the first node (becomes the CA):
homelabmon setup --gen-ca

# Generate a one-time enrollment token:
homelabmon setup --gen-token
# Output: f492e8ac70a9c6f832dfd8dd

# On a new node, enroll using the token:
homelabmon --enroll-url https://192.168.1.10:9600 --enroll-token f492e8ac70a9c6f832dfd8dd
```

After enrollment, all peer communication uses mutual TLS (TLS 1.3, ECDSA P-256). The web dashboard remains accessible via HTTPS without a client certificate.

mTLS is optional -- nodes without certificates communicate over plain HTTP as before.

## Multi-Site Federation

Label nodes with `--site` to group them on the dashboard:

```bash
# Home site
homelabmon --ui --site home --peer 192.168.1.10:9600

# Cloud VPS
homelabmon --site cloud --peer your-home-ip:9600
```

Nodes exchange peer lists via gossip -- connect one node at a remote site to any local node, and all peers discover each other automatically. The dashboard groups hosts by site when multiple sites exist.

You can also set the site label from the Settings page in the web UI.

## Config Hot-Reload

Settings in `~/.homelabmon/config.yaml` are watched for changes. Edits take effect immediately without restarting:

- Log level
- Notification URLs and thresholds
- Metric retention

Settings changed via the web UI are also applied instantly.

## Usage

```bash
homelabmon                                    # bare node: observe + mesh
homelabmon --ui                               # + web dashboard on :9600
homelabmon --ui --no-auth                     # + dashboard without login
homelabmon --scan                             # + network scanning (ARP, mDNS)
homelabmon --llm http://localhost:11434       # + LLM chat (Ollama)
homelabmon --peer 192.168.1.10:9600          # + connect to another node
homelabmon --notify-ntfy https://ntfy.sh/x   # + push notifications
homelabmon --retention-days 30               # keep 30 days of metrics (default: 7)
homelabmon --retention-days 0                # keep metrics forever
homelabmon --site home                      # label this node for multi-site federation
```

All flags can be combined. Every node runs the same binary.

## Three Monitoring Layers

| Layer | Devices | How |
|-------|---------|-----|
| **Agent** | Servers, PCs, NAS, RPi | `homelabmon` runs on the host |
| **Passive** | Phones, tablets, TVs, IoT | ARP/mDNS/SNMP scanning from `--scan` node |
| **Integration** | FRITZ!Box, Unifi, HA, pfSense | API pull via Settings UI |

## Architecture

```
  Node A  <------------>  Node B  <------------>  Node C
  (--ui --llm)            (bare)                  (--ui --scan)
  RPi 4                   NAS                     Mac Mini
```

Each node collects its own metrics and exchanges heartbeats with peers over HTTP. Any node with `--ui` serves the full dashboard showing all nodes.

## Plugin System

Four plugin types, one registry:

| Type | Purpose | Examples |
|------|---------|----------|
| Observer | Local system metrics | CPU, memory, disk, network |
| Probe | Service monitoring | Docker containers |
| Scanner | Network discovery | ARP, mDNS, SNMP |
| Integration | External API pulls | FRITZ!Box, Unifi, HA, Pi-hole, pfSense |

See [PLUGINS.md](PLUGINS.md) for the plugin development guide, including external plugins in any language.

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.23+ |
| Database | SQLite (modernc.org/sqlite, pure Go) |
| Web UI | Go templates + htmx + Alpine.js + Tailwind CSS + Chart.js |
| CLI | cobra + viper |
| Metrics | gopsutil/v4 |
| LLM | Ollama API (local, tool-calling) |

## Cross-Platform Builds

```bash
make all    # builds for linux/amd64, linux/arm64, linux/arm, darwin/amd64, darwin/arm64, windows/amd64
```

## Documentation

- [DESIGN.md](DESIGN.md) -- architecture and design decisions
- [PLUGINS.md](PLUGINS.md) -- plugin development guide
- [PROGRESS.md](PROGRESS.md) -- phase tracker and implementation status
- [CONTRIBUTING.md](CONTRIBUTING.md) -- how to contribute

## License

[GNU Affero General Public License v3.0 (AGPL-3.0)](LICENSE)

You are free to use, modify, and distribute this software. If you modify it and make it available over a network, you must share your changes under the same license.
