# shell-ai-server 🖥

A zero-dependency **Go** HTTP server that exposes shell, file, process, and service-registry APIs for LLM clients to call. Ships as a single static binary — **no Node.js or pm2 required**.

## One-Line Install

```bash
curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/main/install.sh | sh
```

Custom port and install directory:

```bash
curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/main/install.sh | sh -s -- --port 8080 --dir /opt/sas
```

The installer detects your OS (Linux/macOS) and architecture (amd64/arm64), downloads the matching precompiled binary from GitHub Releases, and starts it — via **systemd** on Linux (root) or as a background process elsewhere.

## Quick Start

Build & run with Go:

```bash
go build -o shell-ai-server .
PORT=9100 ./shell-ai-server            # default port 9100
./shell-ai-server                      # custom via PORT env
```

## Run with systemd

```bash
cat > /etc/systemd/system/shell-ai-server.service <<EOF
[Unit]
Description=Shell AI Server (Go)
After=network.target

[Service]
Type=simple
Environment=PORT=9100
ExecStart=/opt/shell-ai-server/shell-ai-server
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now shell-ai-server
journalctl -u shell-ai-server -f
```

## Build for release

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o shell-ai-server_linux_amd64 .
```

## API Docs

- `GET /api` — Full API documentation (JSON)

## Key Endpoints

| Category | Endpoints |
|---|---|
| Shell | `POST /shell/exec`, `POST /shell/spawn`, `GET /shell/tasks` |
| Files | `GET /files`, `POST /files`, `DELETE /files`, `POST /files/search` |
| Processes | `GET /processes`, `POST /processes/:pid/kill` |
| System | `GET /system`, `GET /system/ports`, `GET /system/disk` |
| Services | `POST /services`, `GET /services`, `PUT /services/:id`, `DELETE /services/:id` |
| Network | `GET /net/port/:port`, `POST /net/http`, `GET /net/dns` |

See `GET /api` for the complete endpoint list with request/response examples.

No authentication. Data (service registry, task logs) is stored in the system temp directory (override with `DATA_DIR`).
