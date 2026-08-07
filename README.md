# infra-pxe

Single-binary bare-metal PXE provisioning engine. SQLite embedded, zero external dependencies (except dnsmasq).

## What it does

Power on a server → DHCP → iPXE → kickstart/cloud-init → OS installed. The entire lifecycle managed via REST API.

```
┌─────────┐      DHCP       ┌───────────┐     iPXE menu      ┌──────────┐
│  Server  │ ──────────────▶ │  dnsmasq  │ ──────────────────▶ │ PXE API  │
│ (target) │                 │  (DHCP+   │                     │ (render  │
│          │ ◀────────────── │   TFTP)   │                     │  ks/ci)  │
└─────────┘   iPXE firmware  └───────────┘                     └──────────┘
      │                                                              │
      │              kickstart / cloud-init install                   │
      │◀─────────────────────────────────────────────────────────────┘
      │
      │  POST /api/pxe/event  (装机进度上报)
      │  POST /api/provision/complete  (装机完成)
      ▼
┌──────────┐
│ PXE API  │ ──▶ SQLite (task status, history)
│          │ ──▶ Webhook (optional, forward to CMDB/controller)
└──────────┘
```

## Features

- **Single binary** — Go static build, ~13MB
- **SQLite storage** — zero setup, no external DB
- **REST API** — full CRUD for tasks, OS templates, DHCP config, ISO management
- **MCP Server** — built-in Model Context Protocol endpoint for AI agent integration
- **iPXE rendering** — dynamic boot menu, kickstart/cloud-init template rendering
- **DHCP management** — dnsmasq lifecycle, static bindings, leases
- **Webhook** — optional event forwarding to external systems
- **Seed data** — YAML-based OS template/script definitions, importable via API

## Quick Start

```bash
# Build
make build-linux

# Or package as tarball
make pack

# Deploy to target machine
scp release/dist/infra-pxe-*-linux-arm64.tar.gz root@target:/tmp/
ssh root@target 'tar xzf /tmp/infra-pxe-*.tar.gz -C /tmp && /tmp/infra-pxe/install.sh -i eth0'
```

### Minimal config (`conf/pxe.yaml`)

```yaml
server:
  listen: "0.0.0.0"
  port: 9200

dnsmasq:
  binary: "dnsmasq"
  lease_time: "5m"

data:
  dir: "data"

# Optional: forward events to external system
# webhook:
#   url: "http://your-cmdb:8080/api/hook/pxe-event"
#   token: "bearer-token"
```

### First run

```bash
# Start PXE engine
./bin/infra-pxe --config conf/pxe.yaml

# Configure DHCP (picks up interface and IP range)
curl -X PUT http://localhost:9200/api/dhcp/config \
  -H "Content-Type: application/json" \
  -d '{"interface":"eth0","dhcp_start":"10.0.0.100","dhcp_end":"10.0.0.200","netmask":"255.255.255.0","gateway":"10.0.0.1"}'

# Import seed data (OS templates, scripts)
curl -X POST http://localhost:9200/api/seed/import

# Create a deploy task
curl -X POST http://localhost:9200/api/tasks \
  -H "Content-Type: application/json" \
  -d '{"sn":"SVR001","mac":"aa:bb:cc:dd:ee:ff","os_template_bid":"tpl-euler03x64-std","ip":"10.0.0.50"}'
```

Now PXE boot the target machine — it will get an IP, load iPXE, render the kickstart, and install the OS automatically.

## API Overview

| Category | Endpoints |
|----------|-----------|
| **Tasks** | `POST/GET/PUT/DELETE /api/tasks`, `POST /api/tasks/batch` |
| **OS Templates** | `POST/GET/DELETE /api/os-templates` |
| **Templates** | `POST/GET/DELETE /api/templates` (kickstart/cloud-init) |
| **DHCP** | `GET/PUT /api/dhcp/config`, `POST/GET/DELETE /api/dhcp/bindings`, `GET /api/dhcp/leases` |
| **ISO** | `GET /api/iso/list`, `POST /api/iso/mount`, `POST /api/iso/download` |
| **Files** | `GET/POST/DELETE /api/files` |
| **Provision** | `GET /api/provision/by-sn/{sn}`, `GET /api/provision/by-mac/{mac}`, `POST /api/provision/complete` |
| **Events** | `POST /api/pxe/event` |
| **Results** | `GET /api/results` |
| **System** | `GET /api/status`, `GET /api/health`, `POST /api/dnsmasq/start\|stop\|reload` |
| **Seeds** | `POST /api/seed/import` |
| **MCP** | `/mcp` (Streamable HTTP) |
| **Boot render** | `GET /render/menu.ipxe`, `GET /render/ks/{os_id}`, `GET /render/cloud-init/{os_id}/user-data` |

## Directory Structure

```
infra-pxe/
├── main.go                    Entry point
├── internal/
│   ├── config/                Configuration loading
│   ├── db/                    SQLite schema + queries
│   ├── store/                 Business logic layer
│   ├── handler/               HTTP handlers (REST API)
│   ├── dnsmasq/               dnsmasq process management
│   ├── mcpserver/             MCP protocol server
│   └── logger/                Structured logging
├── boot/tftp/                 iPXE firmware (EFI64, EFI ARM, BIOS)
├── templates/                 Kickstart/cloud-init templates + scripts
├── seeds/                     OS template & script seed definitions
├── conf/pxe.yaml.example     Configuration example
├── scripts/dhcp-event.sh      dnsmasq DHCP hook
└── release/                   Build & deploy scripts
## AI Agent Usage

This repo includes a Claude Code skill for automating PXE operations via the `infra-pxe` MCP server.

### Install

Clone the repo, then symlink or copy the skill to your Claude Code skills directory:

```bash
# Symlink (recommended — auto-updates when you git pull)
ln -s $(pwd)/skills/infra-pxe ~/.claude/skills/infra-pxe

# Or copy
cp -r skills/infra-pxe ~/.claude/skills/
```

Then configure the MCP server in your `.claude/mcp.json` (or project `.mcp.json`):

```json
{
  "mcpServers": {
    "infra-pxe": {
      "type": "http",
      "url": "http://<your-worker-ip>:9200/mcp"
    }
  }
}
```

### What it does

The skill (`SKILL.md`) guides Claude through the full PXE lifecycle:

- Deploy worker binary to target machine
- Initialize seed data, DHCP config, ISO mounts
- Create install tasks with network config
- Validate templates before kicking off PXE boot
- Manage DHCP static bindings for fixed IP allocation

See `skills/infra-pxe/references/` for detailed tool reference docs.


## Requirements

- Linux (amd64 or arm64)
- dnsmasq (DHCP + TFTP)
- Target servers with PXE/UEFI network boot support

## License

Apache-2.0
