# shell-ai-server 🖥

A zero-dependency HTTP server that exposes shell, file, process, and service-registry APIs for LLM clients to call.

## One-Line Install

```bash
curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/master/install.sh | bash
```

Custom port and install directory:

```bash
curl -fsSL https://raw.githubusercontent.com/qirong77/shell-ai-server/master/install.sh | bash -s -- --port 8080 --dir /opt/sas
```

The installer auto-detects OS (macOS/Linux), checks/installs Node.js >=18 and pm2, downloads `shell-server.mjs`, and starts the service with pm2.

## Quick Start

```bash
node shell-server.mjs              # default port 9100
PORT=8080 node shell-server.mjs    # custom port
```

## Run with PM2

```bash
pm2 start shell-server.mjs --name shell-ai-server
pm2 start shell-server.mjs --name shell-ai-server -- --port 8080   # custom port
pm2 logs shell-ai-server
pm2 restart shell-ai-server
pm2 stop shell-ai-server
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

No authentication. Data (service registry, task logs) is stored in the system temp directory.
