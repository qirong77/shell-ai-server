#!/usr/bin/env node

/**
 * shell-ai-server — A server-side shell server for LLM clients.
 *
 * No auth. Rich REST API. Standard service registry for recording/querying
 * deployed services.
 *
 * Usage:
 *   node shell-server.mjs                # default port 9100
 *   PORT=8080 node shell-server.mjs      # custom port
 *   node shell-server.mjs --help         # help
 */

import http from "node:http";
import { exec, spawn } from "node:child_process";
import {
  readFileSync, writeFileSync, mkdirSync, existsSync, statSync,
  readdirSync, unlinkSync, renameSync, rmSync, appendFileSync, createWriteStream
} from "node:fs";
import { join, dirname, basename, resolve, sep } from "node:path";
import { homedir, tmpdir, platform, arch, cpus, totalmem, freemem, hostname, uptime as osUptime, loadavg } from "node:os";
import { networkInterfaces } from "node:os";
import { createHash, randomUUID } from "node:crypto";
import { lookup as dnsLookup } from "node:dns/promises";
import { pipeline } from "node:stream/promises";
import net from "node:net";

// ─── Config ───────────────────────────────────────────────────────────────

const PORT = process.env.PORT ? parseInt(process.env.PORT) : 9100;
const HOST = process.env.HOST || "0.0.0.0";
const DATA_DIR = resolve(process.env.DATA_DIR || join(tmpdir(), "shell-ai-server"));
const SERVICES_FILE = join(DATA_DIR, "services.json");
const LOG_DIR = join(DATA_DIR, "logs");
const MAX_BODY_SIZE = 10 * 1024 * 1024; // 10 MB

// Ensure data directories exist
mkdirSync(DATA_DIR, { recursive: true });
mkdirSync(LOG_DIR, { recursive: true });

// ─── Standard Response Envelope ───────────────────────────────────────────
//
// Every API response uses this envelope so LLM clients can reliably parse
// results regardless of which endpoint was called.

function ok(data, meta = {}) {
  return {
    ok: true,
    data,
    meta: { timestamp: new Date().toISOString(), ...meta },
  };
}

function fail(error, code = "ERROR", status = 400, details = null) {
  return {
    ok: false,
    error: { code, message: error, details },
    meta: { timestamp: new Date().toISOString() },
  };
}

// ─── Service Registry (Standard Storage & Query Format) ───────────────────
//
// A persistent JSON store for recording services that an LLM deploys.
// Standard record format:
// {
//   id, name, type, port, pid, status, healthCheck, tags, config, metadata,
//   deployedAt, updatedAt, workingDir, startCommand, stopCommand, logPath
// }

const SERVICE_TYPES = ["web", "api", "database", "cache", "queue", "worker", "proxy", "custom"];
const SERVICE_STATUSES = ["running", "stopped", "error", "deploying", "unknown"];

class ServiceRegistry {
  constructor(filePath) {
    this.filePath = filePath;
    this.services = new Map();
    this._load();
  }

  _load() {
    if (existsSync(this.filePath)) {
      try {
        const raw = readFileSync(this.filePath, "utf-8");
        const arr = JSON.parse(raw);
        if (Array.isArray(arr)) {
          for (const s of arr) this.services.set(s.id, s);
        }
      } catch (err) {
        try {
          renameSync(this.filePath, `${this.filePath}.corrupt-${Date.now()}`);
        } catch {}
        console.warn(`[ServiceRegistry] Could not load registry: ${err.message}`);
      }
    }
  }

  _persist() {
    const arr = [...this.services.values()];
    const tempPath = `${this.filePath}.${process.pid}.tmp`;
    writeFileSync(tempPath, JSON.stringify(arr, null, 2), "utf-8");
    renameSync(tempPath, this.filePath);
  }

  /**
   * Register a new service.
   * @param {object} rec — partial service record
   * @returns {object} the created record (with generated id, timestamps)
   */
  register(rec) {
    const now = new Date().toISOString();
    const id = rec.id || randomUUID();
    const record = {
      id,
      name: rec.name || "unnamed-service",
      type: SERVICE_TYPES.includes(rec.type) ? rec.type : "custom",
      port: rec.port ?? null,
      pid: rec.pid ?? null,
      host: rec.host || "localhost",
      status: SERVICE_STATUSES.includes(rec.status) ? rec.status : "unknown",
      healthCheck: {
        type: rec.healthCheck?.type || "none",
        url: rec.healthCheck?.url || null,
        interval: rec.healthCheck?.interval ?? 30,
        ...rec.healthCheck,
      },
      tags: Array.isArray(rec.tags) ? rec.tags : [],
      config: rec.config || {},
      metadata: rec.metadata || {},
      workingDir: rec.workingDir || null,
      startCommand: rec.startCommand || null,
      stopCommand: rec.stopCommand || null,
      logPath: rec.logPath || null,
      deployedAt: rec.deployedAt || now,
      updatedAt: now,
    };
    this.services.set(id, record);
    this._persist();
    return record;
  }

  /**
   * Query services with flexible filters.
   * @param {object} filters — { name, type, status, tag, port, pid, host }
   * @returns {array} matching records
   */
  query(filters = {}) {
    let results = [...this.services.values()];
    const { name, type, status, tag, port, pid, host, q } = filters;

    if (q) {
      const ql = q.toLowerCase();
      results = results.filter(s =>
        s.name.toLowerCase().includes(ql) ||
        s.tags.some(t => t.toLowerCase().includes(ql)) ||
        (s.metadata && JSON.stringify(s.metadata).toLowerCase().includes(ql))
      );
    }
    if (name) results = results.filter(s => s.name === name);
    if (type) results = results.filter(s => s.type === type);
    if (status) results = results.filter(s => s.status === status);
    if (tag) results = results.filter(s => s.tags.includes(tag));
    if (port != null) results = results.filter(s => s.port === Number(port));
    if (pid != null) results = results.filter(s => s.pid === Number(pid));
    if (host) results = results.filter(s => s.host === host);

    return results;
  }

  get(id) {
    return this.services.get(id) || null;
  }

  update(id, patch) {
    const existing = this.services.get(id);
    if (!existing) return null;
    const updated = {
      ...existing,
      ...patch,
      healthCheck: { ...existing.healthCheck, ...(patch.healthCheck || {}) },
      tags: Array.isArray(patch.tags) ? patch.tags : existing.tags,
      updatedAt: new Date().toISOString(),
    };
    this.services.set(id, updated);
    this._persist();
    return updated;
  }

  delete(id) {
    const existed = this.services.delete(id);
    if (existed) this._persist();
    return existed;
  }

  clear() {
    this.services.clear();
    this._persist();
  }

  size() {
    return this.services.size;
  }
}

const registry = new ServiceRegistry(SERVICES_FILE);

// ─── Health Monitor ────────────────────────────────────────────────────────
//
// Periodically checks all registered services and auto-updates their status.
// Checks: PID alive, port open, HTTP health endpoint.
// Respects per-service healthCheck.interval (min 10s, default 30s).

class HealthMonitor {
  constructor(registry) {
    this.registry = registry;
    this.timers = new Map();   // serviceId -> setInterval handle
    this.history = new Map();  // serviceId -> [{ time, healthy, checks }]
    this.maxHistory = 20;
    this.started = false;
    this.checking = new Set();
  }

  start() {
    if (this.started) return;
    this.started = true;
    this._rescheduleAll();
    console.log("[HealthMonitor] Started — monitoring " + this.registry.size() + " services");
  }

  stop() {
    for (const [id, timer] of this.timers) {
      clearInterval(timer);
    }
    this.timers.clear();
    this.started = false;
  }

  _rescheduleAll() {
    for (const service of this.registry.query()) {
      this._rescheduleOne(service);
    }
  }

  _rescheduleOne(service) {
    // Clear existing timer
    if (this.timers.has(service.id)) {
      clearInterval(this.timers.get(service.id));
    }

    const intervalSec = Math.max(
      10,
      service.healthCheck?.interval || 30
    );
    const intervalMs = intervalSec * 1000;

    const timer = setInterval(() => this._check(service.id), intervalMs);
    this.timers.set(service.id, timer);
  }

  async _check(serviceId) {
    if (this.checking.has(serviceId)) return;
    this.checking.add(serviceId);
    try {
    const record = this.registry.get(serviceId);
    if (!record) {
      // Service deleted — stop monitoring
      if (this.timers.has(serviceId)) {
        clearInterval(this.timers.get(serviceId));
        this.timers.delete(serviceId);
      }
      this.history.delete(serviceId);
      return;
    }

    const checks = { pidAlive: false, portOpen: false, httpOk: null };

    // PID check
    if (record.pid) {
      try {
        process.kill(record.pid, 0);
        checks.pidAlive = true;
      } catch { checks.pidAlive = false; }
    }

    // Port check
    if (record.port) {
      checks.portOpen = await checkPort(record.host || "localhost", record.port);
    }

    // HTTP health check
    if (record.healthCheck?.url) {
      try {
        const resp = await fetch(record.healthCheck.url, { signal: AbortSignal.timeout(5000) });
        checks.httpOk = { status: resp.status, ok: resp.ok };
      } catch (err) {
        checks.httpOk = { ok: false, error: err.message };
      }
    }

    const healthy = !!(checks.pidAlive || checks.portOpen || checks.httpOk?.ok);
    const newStatus = healthy ? "running" : "error";

    // Auto-update if status changed
    if (record.status !== newStatus) {
      this.registry.update(serviceId, {
        status: newStatus,
        metadata: { ...record.metadata, lastHealthCheck: new Date().toISOString(), lastHealthStatus: newStatus },
      });
    }

    // Record history
    const hist = this.history.get(serviceId) || [];
    hist.push({ time: new Date().toISOString(), healthy, checks });
    if (hist.length > this.maxHistory) hist.shift();
    this.history.set(serviceId, hist);
    } finally {
      this.checking.delete(serviceId);
    }
  }

  getHistory(serviceId) {
    return this.history.get(serviceId) || [];
  }

  // Called when a service is registered or updated — reschedule if interval changed
  onServiceChanged(serviceId) {
    const record = this.registry.get(serviceId);
    if (record && this.started) {
      this._rescheduleOne(record);
      // Run an immediate check
      this._check(serviceId);
    }
  }
}

const healthMonitor = new HealthMonitor(registry);


// ─── Background Task Manager ──────────────────────────────────────────────
//
// Tracks long-running processes started via /shell/spawn.
// Each task: { id, pid, command, status, startedAt, endedAt, exitCode, logPath }

class TaskManager {
  constructor() {
    this.tasks = new Map();
  }

  start(command, args = [], opts = {}) {
    const id = randomUUID();
    const logPath = join(LOG_DIR, `task-${id}.log`);
    const logStream = createWriteStream(logPath, { flags: "a" });

    const child = spawn(command, args, {
      cwd: opts.cwd || process.cwd(),
      env: { ...process.env, ...(opts.env || {}) },
      shell: opts.shell ?? true,
      detached: false,
      stdio: ["ignore", "pipe", "pipe"],
    });

    const appendLog = (chunk) => {
      if (!logStream.destroyed) logStream.write(chunk);
    };

    child.stdout.on("data", appendLog);
    child.stderr.on("data", appendLog);

    const task = {
      id,
      pid: child.pid,
      command: `${command} ${args.join(" ")}`.trim(),
      args,
      status: "running",
      startedAt: new Date().toISOString(),
      endedAt: null,
      exitCode: null,
      logPath,
      cwd: opts.cwd || process.cwd(),
    };

    child.on("exit", (code, signal) => {
      task.status = signal ? "killed" : code === 0 ? "completed" : "error";
      task.endedAt = new Date().toISOString();
      task.exitCode = code ?? -1;
      task.signal = signal || null;
      logStream.end();
    });
    child.on("error", (err) => {
      task.status = "error";
      task.endedAt = new Date().toISOString();
      task.error = err.message;
      if (!logStream.destroyed) logStream.write(`\n[ERROR] ${err.message}\n`);
    });

    this.tasks.set(id, { task, child, logStream });
    return task;
  }

  get(id) {
    const entry = this.tasks.get(id);
    return entry ? { ...entry.task } : null;
  }

  list() {
    return [...this.tasks.values()].map(({ task }) => ({ ...task }));
  }

  kill(id, signal = "SIGTERM") {
    const entry = this.tasks.get(id);
    if (!entry || entry.task.status !== "running") return false;
    try {
      entry.child.kill(signal);
      return true;
    } catch {
      return false;
    }
  }

  getLog(id, lines = 100) {
    const entry = this.tasks.get(id);
    if (!entry) return null;
    const logPath = entry.task.logPath;
    lines = Math.min(Math.max(Number(lines) || 100, 1), 5000);
    if (existsSync(logPath)) {
      const content = readFileSync(logPath, "utf-8");
      const allLines = content.split("\n");
      return allLines.slice(-lines).join("\n");
    }
    return "";
  }

  cleanup(maxAge = 3600_000) {
    const now = Date.now();
    for (const [id, entry] of this.tasks) {
      if (entry.task.status !== "running" && entry.task.endedAt) {
        const age = now - new Date(entry.task.endedAt).getTime();
        if (age > maxAge) this.tasks.delete(id);
      }
    }
  }
}

const taskManager = new TaskManager();

// ─── HTTP Helpers ──────────────────────────────────────────────────────────

function readBody(req, maxBytes = MAX_BODY_SIZE) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    let settled = false;
    req.on("data", (chunk) => {
      if (settled) return;
      size += chunk.length;
      if (size > maxBytes) {
        settled = true;
        reject(new Error("Request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      if (settled) return;
      settled = true;
      const buf = Buffer.concat(chunks);
      resolve(buf);
    });
    req.on("error", (err) => {
      if (settled) return;
      settled = true;
      reject(err);
    });
  });
}

function parseJSONBody(buf) {
  if (buf.length === 0) return {};
  try {
    return JSON.parse(buf.toString("utf-8"));
  } catch {
    const err = new Error("Invalid JSON body");
    err.code = "BAD_REQUEST";
    err.status = 400;
    throw err;
  }
}

function boundedInt(value, fallback, min = 1, max = 2_147_483_647) {
  const n = Number(value);
  if (!Number.isInteger(n)) return fallback;
  return Math.min(max, Math.max(min, n));
}

function sendJSON(res, obj, status = 200) {
  if (status === 204) {
    res.writeHead(204, {
      "Access-Control-Allow-Origin": "*",
      "Access-Control-Allow-Methods": "GET, POST, PUT, DELETE, OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type, Authorization",
    });
    res.end();
    return;
  }
  const body = JSON.stringify(obj, null, 2);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(body),
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET, POST, PUT, DELETE, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type, Authorization",
  });
  res.end(body);
}

function sendText(res, text, status = 200) {
  res.writeHead(status, {
    "Content-Type": "text/plain; charset=utf-8",
    "Access-Control-Allow-Origin": "*",
  });
  res.end(text);
}

// ─── Router ────────────────────────────────────────────────────────────────

/**
 * Simple pattern-based router.
 * Routes are defined as: { method, pattern, handler }
 * Pattern uses :param for path parameters, e.g. "/files/:path*"
 */

class Router {
  constructor() {
    this.routes = [];
  }

  add(method, pattern, handler) {
    // Convert pattern to regex
    const paramNames = [];
    const regexStr = pattern
      .replace(/:(\w+)\*/g, (_, name) => {
        paramNames.push(name);
        return "(.*)";
      })
      .replace(/:(\w+)/g, (_, name) => {
        paramNames.push(name);
        return "([^/]+)";
      })
      .replace(/\//g, "\\/");

    this.routes.push({
      method: method.toUpperCase(),
      pattern: new RegExp(`^${regexStr}$`),
      paramNames,
      handler,
    });
  }

  match(method, pathname) {
    for (const route of this.routes) {
      if (route.method !== method.toUpperCase()) continue;
      const match = route.pattern.exec(pathname);
      if (match) {
        const params = {};
        route.paramNames.forEach((name, i) => {
          try {
            params[name] = decodeURIComponent(match[i + 1] || "");
          } catch {
            params[name] = match[i + 1] || "";
          }
        });
        return { handler: route.handler, params };
      }
    }
    return null;
  }
}

const router = new Router();

// ─── API Routes ────────────────────────────────────────────────────────────
//
// All routes are organized by category. The full API surface is designed for
// LLM clients to inspect, execute, and manage services on the host.

// ── Root & Health ──────────────────────────────────────────────────────────

router.add("GET", "/", async (req, res) => {
  sendJSON(res, ok({
    name: "shell-ai-server",
    version: "1.0.0",
    description: "A shell server for LLM clients",
    endpoints: getApiSummary(),
    dataDir: DATA_DIR,
    uptime: process.uptime(),
  }));
});

router.add("GET", "/health", async (req, res) => {
  sendJSON(res, ok({ status: "healthy", uptime: process.uptime() }));
});

router.add("GET", "/api", async (req, res) => {
  sendJSON(res, ok(getApiDocs()));
});


// ── Shell Execution ────────────────────────────────────────────────────────

// POST /shell/exec — Execute a command synchronously (with timeout)
router.add("POST", "/shell/exec", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { command, cwd, timeout: requestedTimeout = 30000, env } = body;
  const timeout = boundedInt(requestedTimeout, 30000, 1, 10 * 60 * 1000);

  if (!command) return sendJSON(res, fail("Missing 'command' field", "MISSING_PARAM"), 400);

  const startedAt = process.hrtime.bigint();
  return new Promise((resolve) => {
    const child = exec(command, {
      cwd: cwd || process.cwd(),
      env: { ...process.env, ...(env || {}) },
      timeout,
      maxBuffer: 5 * 1024 * 1024,
    }, (error, stdout, stderr) => {
      const result = {
        command,
        cwd: cwd || process.cwd(),
        exitCode: error ? (typeof error.code === "number" ? error.code : -1) : 0,
        stdout: stdout.toString("utf-8"),
        stderr: stderr.toString("utf-8"),
        timedOut: error?.killed === true,
        duration: Math.round(Number(process.hrtime.bigint() - startedAt) / 1e6),
      };
      if (error && !error.killed) {
        result.error = error.message;
      }
      sendJSON(res, ok(result, { command }));
      resolve();
    });
  });
});

// POST /shell/spawn — Start a background task, returns task ID immediately
router.add("POST", "/shell/spawn", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { command, args = [], cwd, env, shell = true } = body;

  if (!command) return sendJSON(res, fail("Missing 'command' field", "MISSING_PARAM"), 400);

  const task = taskManager.start(command, args, { cwd, env, shell });
  sendJSON(res, ok(task, { taskId: task.id }));
});

// GET /shell/tasks — List all background tasks
router.add("GET", "/shell/tasks", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const status = url.searchParams.get("status");
  let tasks = taskManager.list();
  if (status) tasks = tasks.filter(t => t.status === status);
  sendJSON(res, ok(tasks, { count: tasks.length }));
});

// GET /shell/tasks/:id — Get task details
router.add("GET", "/shell/tasks/:id", async (req, res, params) => {
  const task = taskManager.get(params.id);
  if (!task) return sendJSON(res, fail("Task not found", "NOT_FOUND", 404), 404);
  const url = new URL(req.url, `http://${req.headers.host}`);
  const lines = boundedInt(url.searchParams.get("lines") || 100, 100, 1, 5000);
  const log = taskManager.getLog(params.id, lines);
  sendJSON(res, ok({ ...task, log }));
});

// GET /shell/tasks/:id/log — Get task log (plain text)
router.add("GET", "/shell/tasks/:id/log", async (req, res, params) => {
  const task = taskManager.get(params.id);
  if (!task) return sendJSON(res, fail("Task not found", "NOT_FOUND", 404), 404);
  const url = new URL(req.url, `http://${req.headers.host}`);
  const lines = boundedInt(url.searchParams.get("lines") || 200, 200, 1, 5000);
  const log = taskManager.getLog(params.id, lines);
  sendText(res, log || "(no log output)");
});

// POST /shell/tasks/:id/kill — Kill a background task
router.add("POST", "/shell/tasks/:id/kill", async (req, res, params) => {
  const body = parseJSONBody(await readBody(req));
  const signal = body.signal || "SIGTERM";
  const success = taskManager.kill(params.id, signal);
  if (!success) return sendJSON(res, fail("Task not found or already stopped", "NOT_FOUND", 404), 404);
  sendJSON(res, ok({ killed: true, taskId: params.id, signal }));
});

// DELETE /shell/tasks/:id — Remove a completed task from tracking
router.add("DELETE", "/shell/tasks/:id", async (req, res, params) => {
  const task = taskManager.get(params.id);
  if (!task) return sendJSON(res, fail("Task not found", "NOT_FOUND", 404), 404);
  if (task.status === "running") return sendJSON(res, fail("Task is still running, kill it first", "STILL_RUNNING", 400), 400);
  taskManager.tasks.delete(params.id);
  sendJSON(res, ok({ deleted: true, taskId: params.id }));
});

// POST /shell/script — Execute a multi-line script
router.add("POST", "/shell/script", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { script, cwd, timeout: requestedTimeout = 60000, shell: shellName } = body;
  const timeout = boundedInt(requestedTimeout, 60000, 1, 10 * 60 * 1000);

  if (!script) return sendJSON(res, fail("Missing 'script' field", "MISSING_PARAM"), 400);

  const useShell = shellName || (platform() === "win32" ? "cmd.exe" : "/bin/bash");

  return new Promise((resolve) => {
    const shellArgs = platform() === "win32" ? ["/D", "/S", "/C", script] : ["-e"];
    const child = spawn(useShell, shellArgs, {
      cwd: cwd || process.cwd(),
      env: process.env,
      stdio: ["pipe", "pipe", "pipe"],
    });

    let stdout = "", stderr = "";
    child.stdout.on("data", (d) => stdout += d.toString());
    child.stderr.on("data", (d) => stderr += d.toString());

    const timer = setTimeout(() => {
      child.kill("SIGTERM");
    }, timeout);

    let settled = false;
    const finish = (code, signal, error = null) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      sendJSON(res, ok({
        script,
        shell: useShell,
        exitCode: code ?? -1,
        stdout,
        stderr,
        timedOut: signal === "SIGTERM",
        ...(error ? { error: error.message } : {}),
      }));
      resolve();
    };

    child.on("exit", (code, signal) => finish(code ?? -1, signal));
    child.on("error", (err) => finish(-1, null, err));

    child.stdin.write(script);
    child.stdin.end();
  });
});

// ── File Operations ─────────────────────────────────────────────────────────

// GET /files?path=... — Read file contents
router.add("GET", "/files", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const filePath = url.searchParams.get("path");
  const encoding = url.searchParams.get("encoding") || "utf-8";

  if (!filePath) return sendJSON(res, fail("Missing 'path' query param", "MISSING_PARAM"), 400);

  try {
    const absPath = resolve(filePath);
    const stat = statSync(absPath);
    if (stat.isDirectory()) {
      const entries = readdirSync(absPath, { withFileTypes: true }).map(e => ({
        name: e.name,
        type: e.isDirectory() ? "dir" : e.isFile() ? "file" : e.isSymbolicLink() ? "symlink" : "other",
        size: (() => { try { return statSync(join(absPath, e.name)).size; } catch { return null; } })(),
      }));
      sendJSON(res, ok({ path: absPath, type: "dir", entries }, { count: entries.length }));
    } else {
      const content = readFileSync(absPath, encoding);
      sendJSON(res, ok({ path: absPath, type: "file", size: stat.size, content }, { size: stat.size }));
    }
  } catch (err) {
    sendJSON(res, fail(`File operation failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// POST /files — Write file contents
router.add("POST", "/files", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { path: filePath, content, append = false, encoding = "utf-8" } = body;

  if (!filePath) return sendJSON(res, fail("Missing 'path' field", "MISSING_PARAM"), 400);
  if (content === undefined) return sendJSON(res, fail("Missing 'content' field", "MISSING_PARAM"), 400);

  try {
    const absPath = resolve(filePath);
    const dir = dirname(absPath);
    if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
    if (append) {
      appendFileSync(absPath, content, encoding);
    } else {
      writeFileSync(absPath, content, encoding);
    }
    const stat = statSync(absPath);
    sendJSON(res, ok({ path: absPath, size: stat.size, appended: append }));
  } catch (err) {
    sendJSON(res, fail(`File write failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// POST /files/mkdir — Create directory
router.add("POST", "/files/mkdir", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { path: dirPath, recursive = true } = body;
  if (!dirPath) return sendJSON(res, fail("Missing 'path' field", "MISSING_PARAM"), 400);
  try {
    const absPath = resolve(dirPath);
    mkdirSync(absPath, { recursive });
    sendJSON(res, ok({ path: absPath, created: true }));
  } catch (err) {
    sendJSON(res, fail(`mkdir failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// DELETE /files?path=... — Delete file or directory
router.add("DELETE", "/files", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const filePath = url.searchParams.get("path");
  if (!filePath) return sendJSON(res, fail("Missing 'path' query param", "MISSING_PARAM"), 400);
  try {
    const absPath = resolve(filePath);
    rmSync(absPath, { recursive: true, force: true });
    sendJSON(res, ok({ path: absPath, deleted: true }));
  } catch (err) {
    sendJSON(res, fail(`Delete failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// POST /files/move — Move/rename a file
router.add("POST", "/files/move", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { from, to } = body;
  if (!from || !to) return sendJSON(res, fail("Missing 'from' or 'to' field", "MISSING_PARAM"), 400);
  try {
    const absFrom = resolve(from);
    const absTo = resolve(to);
    const dir = dirname(absTo);
    if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
    renameSync(absFrom, absTo);
    sendJSON(res, ok({ from: absFrom, to: absTo, moved: true }));
  } catch (err) {
    sendJSON(res, fail(`Move failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// GET /files/stat?path=... — Get file stats
router.add("GET", "/files/stat", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const filePath = url.searchParams.get("path");
  if (!filePath) return sendJSON(res, fail("Missing 'path' query param", "MISSING_PARAM"), 400);
  try {
    const absPath = resolve(filePath);
    const stat = statSync(absPath);
    sendJSON(res, ok({
      path: absPath,
      size: stat.size,
      isFile: stat.isFile(),
      isDir: stat.isDirectory(),
      mode: stat.mode.toString(8),
      mtime: stat.mtime.toISOString(),
      birthtime: stat.birthtime.toISOString(),
    }));
  } catch (err) {
    sendJSON(res, fail(`stat failed: ${err.message}`, "FILE_ERROR", 500), 500);
  }
});

// POST /files/search — Search file contents (grep)
router.add("POST", "/files/search", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { pattern, path: searchPath = ".", recursive = true, maxResults = 100 } = body;
  if (!pattern) return sendJSON(res, fail("Missing 'pattern' field", "MISSING_PARAM"), 400);

  try {
    const results = grepSearch(resolve(searchPath), new RegExp(pattern), recursive, maxResults);
    sendJSON(res, ok(results, { count: results.length }));
  } catch (err) {
    sendJSON(res, fail(`Search failed: ${err.message}`, "SEARCH_ERROR", 500), 500);
  }
});

function grepSearch(dir, regex, recursive, maxResults, results = []) {
  if (results.length >= maxResults) return results;
  const entries = readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    if (results.length >= maxResults) break;
    const fullPath = join(dir, entry.name);
    if (entry.isDirectory() && recursive) {
      if (!entry.name.startsWith(".") && entry.name !== "node_modules") {
        grepSearch(fullPath, regex, recursive, maxResults, results);
      }
    } else if (entry.isFile()) {
      try {
        const content = readFileSync(fullPath, "utf-8");
        const lines = content.split("\n");
        for (let i = 0; i < lines.length; i++) {
          if (regex.test(lines[i])) {
            results.push({ file: fullPath, line: i + 1, content: lines[i].trim() });
            if (results.length >= maxResults) break;
          }
        }
      } catch { /* skip binary or unreadable files */ }
    }
  }
  return results;
}

// ── Process Management ─────────────────────────────────────────────────────

// GET /processes — List processes (uses ps)
router.add("GET", "/processes", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const filter = url.searchParams.get("filter") || "";
  return new Promise((resolve) => {
    const cmd = platform() === "win32"
      ? `tasklist /FO CSV`
      : `ps aux ${filter ? `| grep ${filter} | grep -v grep` : ""}`;

    exec(cmd, { maxBuffer: 2 * 1024 * 1024 }, (error, stdout, stderr) => {
      if (error && !stdout) {
        sendJSON(res, fail(`Failed to list processes: ${stderr || error.message}`, "PROC_ERROR", 500), 500);
      } else {
        const lines = stdout.trim().split("\n");
        const processes = lines.slice(0, 200).map(line => {
          if (platform() === "win32") {
            const parts = line.replace(/"/g, "").split(",");
            return { name: parts[0], pid: parseInt(parts[1]) || 0, session: parts[2], mem: parts[4] };
          }
          const parts = line.trim().split(/\s+/);
          return {
            user: parts[0], pid: parseInt(parts[1]) || 0, cpu: parts[2],
            mem: parts[3], vsz: parts[4], rss: parts[5],
            tty: parts[6], stat: parts[7], start: parts[8],
            command: parts.slice(9).join(" "),
          };
        });
        sendJSON(res, ok(processes, { count: processes.length }));
      }
      resolve();
    });
  });
});

// POST /processes/:pid/kill — Kill a process by PID
router.add("POST", "/processes/:pid/kill", async (req, res, params) => {
  const pid = parseInt(params.pid);
  if (!pid) return sendJSON(res, fail("Invalid PID", "INVALID_PARAM"), 400);
  const body = parseJSONBody(await readBody(req));
  const signal = body.signal || "SIGTERM";
  try {
    process.kill(pid, signal);
    sendJSON(res, ok({ pid, signal, killed: true }));
  } catch (err) {
    sendJSON(res, fail(`Failed to kill process ${pid}: ${err.message}`, "PROC_ERROR", 500), 500);
  }
});

// ── System Information ──────────────────────────────────────────────────────

// GET /system — Full system info
router.add("GET", "/system", async (req, res) => {
  const nets = networkInterfaces();
  const netInfo = {};
  for (const [name, addrs] of Object.entries(nets)) {
    netInfo[name] = (addrs || []).filter(a => !a.internal).map(a => ({
      address: a.address, family: a.family, mac: a.mac,
    }));
  }

  sendJSON(res, ok({
    hostname: hostname(),
    platform: platform(),
    arch: arch(),
    cpus: cpus().map(c => ({ model: c.model, speed: c.speed })),
    cpuCount: cpus().length,
    totalMemory: totalmem(),
    freeMemory: freemem(),
    uptime: osUptime(),
    loadavg: loadavg(),
    network: netInfo,
    homedir: homedir(),
    tmpdir: tmpdir(),
    nodeVersion: process.version,
    pid: process.pid,
    cwd: process.cwd(),
    env: {
      PATH: process.env.PATH,
      SHELL: process.env.SHELL,
      HOME: process.env.HOME,
      USER: process.env.USER,
    },
  }));
});

// GET /system/ports — Check which ports are in use
router.add("GET", "/system/ports", async (req, res) => {
  return new Promise((resolve) => {
    const cmd = platform() === "win32"
      ? "netstat -ano | findstr LISTENING"
      : "lsof -i -P -n | grep LISTEN 2>/dev/null || ss -tlnp 2>/dev/null";

    exec(cmd, { maxBuffer: 2 * 1024 * 1024 }, (error, stdout) => {
      const ports = [];
      const lines = stdout.trim().split("\n").filter(Boolean);
      for (const line of lines) {
        const match = line.match(/:(\d+)\s/);
        if (match) {
          const port = parseInt(match[1]);
          if (!ports.find(p => p.port === port)) {
            // lsof: COMMAND PID USER ... ; netstat/ss: PID is at end or in last columns
            const parts = line.trim().split(/\s+/);
            let pid = null;
            if (platform() === "win32") {
              // netstat -ano: PID is last column
              pid = parseInt(parts[parts.length - 1]) || null;
            } else {
              // lsof: PID is 2nd column; ss: extract from users:(("proc",pid=123))
              pid = parseInt(parts[1]) || null;
            }
            ports.push({
              port,
              pid,
              raw: line.trim(),
            });
          }
        }
      }
      ports.sort((a, b) => a.port - b.port);
      sendJSON(res, ok(ports, { count: ports.length }));
      resolve();
    });
  });
});

// GET /system/disk — Disk usage
router.add("GET", "/system/disk", async (req, res) => {
  return new Promise((resolve) => {
    const cmd = platform() === "win32"
      ? "wmic logicaldisk get size,freespace,caption"
      : "df -h";
    exec(cmd, (error, stdout) => {
      if (error) {
        sendJSON(res, fail(`Disk query failed: ${error.message}`, "DISK_ERROR", 500), 500);
      } else {
        sendJSON(res, ok({ raw: stdout.trim(), parsed: parseDiskUsage(stdout) }));
      }
      resolve();
    });
  });
});

function parseDiskUsage(output) {
  if (platform() === "win32") {
    const lines = output.trim().split("\n").slice(1);
    return lines.map(l => {
      const parts = l.trim().split(/\s+/);
      return { filesystem: parts[0], free: parts[1], size: parts[2] };
    }).filter(d => d.filesystem);
  }
  const lines = output.trim().split("\n").slice(1);
  return lines.map(l => {
    const parts = l.trim().split(/\s+/);
    // Linux df: Filesystem Size Used Avail Use% Mounted
    // macOS df: Filesystem Size Used Avail Capacity iused ifree %iused Mounted
    // Mounted is always the last column; usePercent is the one with %
    if (parts.length >= 6) {
      const mounted = parts[parts.length - 1];
      const usePercent = parts.find(p => p.includes("%")) || parts[4];
      return {
        filesystem: parts[0], size: parts[1], used: parts[2],
        avail: parts[3], usePercent, mounted,
      };
    }
    return { filesystem: parts[0], raw: l.trim() };
  });
}

// ── Service Registry ────────────────────────────────────────────────────────

// POST /services — Register a new service
router.add("POST", "/services", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const record = registry.register(body);
  healthMonitor.onServiceChanged(record.id);
  sendJSON(res, ok(record, { serviceId: record.id }), 201);
});

// GET /services — Query services with filters
router.add("GET", "/services", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const filters = {};
  for (const key of ["name", "type", "status", "tag", "port", "pid", "host", "q"]) {
    const val = url.searchParams.get(key);
    if (val) filters[key] = val;
  }
  const results = registry.query(filters);
  sendJSON(res, ok(results, { count: results.length, filters }));
});

// GET /services/monitor/status — Get overall monitoring status
router.add("GET", "/services/monitor/status", async (req, res) => {
  const services = registry.query();
  const summary = services.map(s => {
    const hist = healthMonitor.getHistory(s.id);
    const last = hist.length > 0 ? hist[hist.length - 1] : null;
    return {
      id: s.id,
      name: s.name,
      status: s.status,
      monitoring: healthMonitor.timers.has(s.id),
      intervalSec: Math.max(10, s.healthCheck?.interval || 30),
      lastCheck: last?.time || s.metadata?.lastHealthCheck || null,
      lastHealthStatus: last ? (last.healthy ? "running" : "error") : (s.metadata?.lastHealthStatus || null),
      checksCount: hist.length,
    };
  });
  const monitoring = summary.filter(s => s.monitoring).length;
  const healthy = summary.filter(s => s.status === "running").length;
  const errored = summary.filter(s => s.status === "error").length;
  sendJSON(res, ok({
    monitoring,
    total: services.length,
    healthy,
    errored,
    services: summary,
  }));
});

// GET /services/:id — Get a specific service
router.add("GET", "/services/:id", async (req, res, params) => {
  const record = registry.get(params.id);
  if (!record) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);
  sendJSON(res, ok(record));
});

// PUT /services/:id — Update a service record
router.add("PUT", "/services/:id", async (req, res, params) => {
  const body = parseJSONBody(await readBody(req));
  const updated = registry.update(params.id, body);
  if (!updated) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);
  healthMonitor.onServiceChanged(params.id);
  sendJSON(res, ok(updated));
});

// DELETE /services/:id — Delete a service record
router.add("DELETE", "/services/:id", async (req, res, params) => {
  const deleted = registry.delete(params.id);
  if (!deleted) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);
  if (healthMonitor.timers.has(params.id)) {
    clearInterval(healthMonitor.timers.get(params.id));
    healthMonitor.timers.delete(params.id);
  }
  healthMonitor.history.delete(params.id);
  sendJSON(res, ok({ deleted: true, id: params.id }));
});

// POST /services/:id/start — Mark service as deploying then running, optionally start its command
router.add("POST", "/services/:id/start", async (req, res, params) => {
  const record = registry.get(params.id);
  if (!record) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);

  const body = parseJSONBody(await readBody(req));
  let task = null;

  // If a startCommand is provided, spawn it as a background task
  const cmd = body.command || record.startCommand;
  if (cmd) {
    // Use shell mode so the full command string is interpreted by the shell
    // instead of naively splitting on spaces (which breaks quoted args)
    task = taskManager.start(cmd, [], {
      shell: true,
      cwd: body.cwd || record.workingDir || undefined,
    });
    registry.update(params.id, {
      pid: task.pid,
      status: "running",
      metadata: { ...record.metadata, taskId: task.id },
    });
  } else {
    registry.update(params.id, { status: "running" });
  }

  const updated = registry.get(params.id);
  sendJSON(res, ok({ ...updated, task }));
});

// POST /services/:id/stop — Stop a service (kill its PID if available)
router.add("POST", "/services/:id/stop", async (req, res, params) => {
  const record = registry.get(params.id);
  if (!record) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);

  const body = parseJSONBody(await readBody(req));
  const signal = body.signal || "SIGTERM";
  let killed = false;

  if (record.pid) {
    try {
      process.kill(record.pid, signal);
      killed = true;
    } catch (err) {
      // Process may already be dead
    }
  }

  // Also kill associated task if exists
  if (record.metadata?.taskId) {
    taskManager.kill(record.metadata.taskId, signal);
  }

  registry.update(params.id, { status: "stopped", pid: null });
  const updated = registry.get(params.id);
  sendJSON(res, ok({ ...updated, killed }));
});

// GET /services/:id/health — Check service health
router.add("GET", "/services/:id/health", async (req, res, params) => {
  const record = registry.get(params.id);
  if (!record) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);

  const checks = { pidAlive: false, portOpen: false, httpOk: null };

  // Check PID
  if (record.pid) {
    try {
      process.kill(record.pid, 0);
      checks.pidAlive = true;
    } catch { checks.pidAlive = false; }
  }

  // Check port
  if (record.port) {
    checks.portOpen = await checkPort(record.host || "localhost", record.port);
  }

  // HTTP health check
  if (record.healthCheck?.url) {
    try {
      const resp = await fetch(record.healthCheck.url, { signal: AbortSignal.timeout(5000) });
      checks.httpOk = { status: resp.status, ok: resp.ok };
    } catch (err) {
      checks.httpOk = { ok: false, error: err.message };
    }
  }

  // Auto-update status based on checks
  const healthy = checks.pidAlive || checks.portOpen || checks.httpOk?.ok;
  registry.update(params.id, { status: healthy ? "running" : "error" });

  sendJSON(res, ok({ service: registry.get(params.id), checks, healthy: !!healthy }));
});

// GET /services/:id/health-history — Get health check history
router.add("GET", "/services/:id/health-history", async (req, res, params) => {
  const record = registry.get(params.id);
  if (!record) return sendJSON(res, fail("Service not found", "NOT_FOUND", 404), 404);
  const history = healthMonitor.getHistory(params.id);
  sendJSON(res, ok({
    serviceId: params.id,
    serviceName: record.name,
    currentStatus: record.status,
    monitoring: healthMonitor.timers.has(params.id),
    intervalSec: Math.max(10, record.healthCheck?.interval || 30),
    history,
  }, { count: history.length }));
});

// DELETE /services — Clear all services
router.add("DELETE", "/services", async (req, res) => {
  const count = registry.size();
  registry.clear();
  healthMonitor.stop();
  healthMonitor.history.clear();
  sendJSON(res, ok({ cleared: true, count }));
});

// ── Network Tools ──────────────────────────────────────────────────────────

// GET /net/port/:port — Check if a port is open
router.add("GET", "/net/port/:port", async (req, res, params) => {
  const port = parseInt(params.port);
  const url = new URL(req.url, `http://${req.headers.host}`);
  const host = url.searchParams.get("host") || "localhost";
  const open = await checkPort(host, port);
  sendJSON(res, ok({ host, port, open }));
});

// POST /net/http — HTTP proxy request (let LLM make outbound HTTP via server)
router.add("POST", "/net/http", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { url, method = "GET", headers = {}, body: reqBody, timeout = 10000 } = body;

  if (!url) return sendJSON(res, fail("Missing 'url' field", "MISSING_PARAM"), 400);

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), boundedInt(timeout, 10000, 1, 10 * 60 * 1000));
  try {
    const fetchOpts = {
      method,
      headers,
      signal: controller.signal,
    };
    if (reqBody && method !== "GET") {
      fetchOpts.body = typeof reqBody === "string" ? reqBody : JSON.stringify(reqBody);
    }
    const resp = await fetch(url, fetchOpts);
    const text = await resp.text();
    sendJSON(res, ok({
      status: resp.status,
      statusText: resp.statusText,
      headers: Object.fromEntries(resp.headers.entries()),
      body: text,
    }));
  } catch (err) {
    sendJSON(res, fail(`HTTP request failed: ${err.message}`, "HTTP_ERROR", 502), 502);
  } finally {
    clearTimeout(timer);
  }
});

// GET /net/dns?domain=... — DNS lookup
router.add("GET", "/net/dns", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const domain = url.searchParams.get("domain");
  if (!domain) return sendJSON(res, fail("Missing 'domain' query param", "MISSING_PARAM"), 400);
  try {
    const addresses = await dnsLookup(domain, { all: true });
    sendJSON(res, ok({ domain, addresses }));
  } catch (err) {
    sendJSON(res, fail(`DNS lookup failed: ${err.message}`, "DNS_ERROR", 500), 500);
  }
});

// ── Environment ────────────────────────────────────────────────────────────

// GET /env — Get environment variables
router.add("GET", "/env", async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const key = url.searchParams.get("key");
  if (key) {
    sendJSON(res, ok({ key, value: process.env[key] ?? null }));
  } else {
    sendJSON(res, ok(process.env));
  }
});

// POST /env — Set environment variable (for this process and spawned children)
router.add("POST", "/env", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { key, value } = body;
  if (!key) return sendJSON(res, fail("Missing 'key' field", "MISSING_PARAM"), 400);
  process.env[key] = value;
  sendJSON(res, ok({ key, value, set: true }));
});

// ── Batch Operations ───────────────────────────────────────────────────────

// POST /batch — Execute multiple operations in sequence
router.add("POST", "/batch", async (req, res) => {
  const body = parseJSONBody(await readBody(req));
  const { operations = [] } = body;
  if (!Array.isArray(operations)) return sendJSON(res, fail("'operations' must be an array", "INVALID_PARAM"), 400);

  const results = [];
  for (const op of operations) {
    const { type, command, args = [], cwd, env, path: filePath, content } = op;
    try {
      let result;
      switch (type) {
        case "exec":
          result = await execAsync(command, { cwd, env });
          break;
        case "spawn":
          result = taskManager.start(command, args, { cwd, env });
          break;
        case "read":
          result = readFileSync(resolve(filePath), "utf-8");
          break;
        case "write":
          writeFileSync(resolve(filePath), content, "utf-8");
          result = { written: true };
          break;
        case "mkdir":
          mkdirSync(resolve(filePath), { recursive: true });
          result = { created: true };
          break;
        default:
          result = { error: `Unknown operation type: ${type}` };
      }
      results.push({ ok: true, type, result });
    } catch (err) {
      results.push({ ok: false, type, error: err.message });
    }
  }
  sendJSON(res, ok(results, { count: results.length }));
});

// ── API Documentation ──────────────────────────────────────────────────────

function getApiSummary() {
  return [
    "GET  /                     — Server info & API summary",
    "GET  /health               — Health check",
    "GET  /api                  — Full API documentation (JSON)",
    "",
    "Shell:",
    "POST /shell/exec           — Execute command (sync, with timeout)",
    "POST /shell/spawn          — Start background task (async)",
    "GET  /shell/tasks          — List background tasks",
    "GET  /shell/tasks/:id      — Get task details + log",
    "GET  /shell/tasks/:id/log  — Get task log (plain text)",
    "POST /shell/tasks/:id/kill — Kill a background task",
    "DELETE /shell/tasks/:id    — Remove completed task",
    "POST /shell/script         — Execute multi-line script",
    "",
    "Files:",
    "GET  /files?path=          — Read file or list directory",
    "POST /files                — Write/create file",
    "POST /files/mkdir          — Create directory",
    "DELETE /files?path=        — Delete file or directory",
    "POST /files/move           — Move/rename file",
    "GET  /files/stat?path=     — Get file stats",
    "POST /files/search         — Search file contents (grep)",
    "",
    "Processes:",
    "GET  /processes            — List running processes",
    "POST /processes/:pid/kill  — Kill process by PID",
    "",
    "System:",
    "GET  /system               — Full system information",
    "GET  /system/ports         — List listening ports",
    "GET  /system/disk          — Disk usage",
    "",
    "Services:",
    "POST /services             — Register a service",
    "GET  /services             — Query services (filters: name,type,status,tag,port,pid,host,q)",
    "GET  /services/:id         — Get service by ID",
    "PUT  /services/:id         — Update service record",
    "DELETE /services/:id       — Delete service record",
    "POST /services/:id/start   — Start a registered service",
    "POST /services/:id/stop    — Stop a registered service",
    "GET  /services/:id/health          — Health check a service",
    "GET  /services/:id/health-history  — Health check history",
    "GET  /services/monitor/status      — Overall monitoring status",
    "DELETE /services           — Clear all services",
    "",
    "Network:",
    "GET  /net/port/:port       — Check if port is open",
    "POST /net/http             — HTTP proxy request",
    "GET  /net/dns?domain=      — DNS lookup",
    "",
    "Environment:",
    "GET  /env                  — Get environment variables",
    "POST /env                  — Set environment variable",
    "",
    "Batch:",
    "POST /batch                — Execute multiple operations",
  ];
}

function getApiDocs() {
  return {
    title: "Shell-AI-Server API Documentation",
    version: "1.0.0",
    description: "A server-side shell server for LLM clients. No authentication required. All endpoints accept and return JSON with a standard envelope. Base URL: http://<host>:9100",
    standardResponse: {
      description: "Every API response uses this envelope",
      success: { ok: true, data: {}, meta: { timestamp: "ISO-8601" } },
      error: { ok: false, error: { code: "ERROR_CODE", message: "description" }, meta: { timestamp: "ISO-8601" } },
    },
    categories: [
      {
        name: "Shell Execution",
        description: "Execute shell commands on the host. Supports synchronous execution with timeout, background tasks with log tracking, and multi-line scripts.",
        endpoints: [
          {
            method: "POST", path: "/shell/exec",
            summary: "Execute a command synchronously and return stdout/stderr",
            params: { command: "string (required) — the shell command to execute", cwd: "string — working directory (default: server cwd)", timeout: "number — max execution time in ms (default: 30000)", env: "object — extra environment variables" },
            requestExample: { command: "echo hello && whoami", cwd: "/tmp", timeout: 10000 },
            responseExample: { ok: true, data: { command: "echo hello && whoami", cwd: "/tmp", exitCode: 0, stdout: "hello\nqironglin\n", stderr: "", timedOut: false, duration: 0 } },
          },
          {
            method: "POST", path: "/shell/spawn",
            summary: "Start a long-running background task. Returns a task ID immediately for async tracking.",
            params: { command: "string (required) — executable name or command", args: "string[] — command arguments", cwd: "string — working directory", env: "object — environment variables", shell: "boolean — run in shell (default: true)" },
            requestExample: { command: "node", args: ["server.js"], cwd: "/myapp" },
            responseExample: { ok: true, data: { id: "task-uuid", pid: 12345, command: "node server.js", status: "running", startedAt: "2024-01-01T00:00:00Z", logPath: "data/logs/task-uuid.log" } },
          },
          {
            method: "GET", path: "/shell/tasks",
            summary: "List all background tasks (running and completed)",
            params: { status: "query param — filter by status: running, completed, killed, error" },
            responseExample: { ok: true, data: [{ id: "task-uuid", pid: 12345, command: "node server.js", status: "running", startedAt: "..." }], meta: { count: 1 } },
          },
          {
            method: "GET", path: "/shell/tasks/:id",
            summary: "Get details and recent log output of a specific task",
            params: { id: "path param — task UUID", lines: "query param — number of log lines to return (default: 100)" },
            responseExample: { ok: true, data: { id: "task-uuid", pid: 12345, status: "completed", exitCode: 0, log: "Server started on port 3000\n..." } },
          },
          {
            method: "GET", path: "/shell/tasks/:id/log",
            summary: "Get task log as plain text",
            params: { id: "path param — task UUID", lines: "query param — number of lines (default: 200)" },
            responseExample: "Server started on port 3000\nGET /health 200 2ms",
          },
          {
            method: "POST", path: "/shell/tasks/:id/kill",
            summary: "Send a signal to terminate a background task",
            params: { id: "path param — task UUID", signal: "body field — signal name (default: SIGTERM)" },
            requestExample: { signal: "SIGKILL" },
            responseExample: { ok: true, data: { killed: true, taskId: "task-uuid", signal: "SIGKILL" } },
          },
          {
            method: "DELETE", path: "/shell/tasks/:id",
            summary: "Remove a completed task from the tracking list (must be stopped first)",
            responseExample: { ok: true, data: { deleted: true, taskId: "task-uuid" } },
          },
          {
            method: "POST", path: "/shell/script",
            summary: "Execute a multi-line shell script (writes to stdin of /bin/bash -e)",
            params: { script: "string (required) — multi-line script content", cwd: "string — working directory", timeout: "number — max time in ms (default: 60000)", shell: "string — shell path (default: /bin/bash or cmd.exe)" },
            requestExample: { script: "cd /tmp\necho 'step 1'\nmkdir -p test\necho 'done'" },
            responseExample: { ok: true, data: { exitCode: 0, stdout: "step 1\ndone\n", stderr: "", timedOut: false } },
          },
        ],
      },
      {
        name: "File Operations",
        description: "Read, write, move, delete files and directories. Search file contents with regex.",
        endpoints: [
          {
            method: "GET", path: "/files?path=",
            summary: "Read file contents (returns content) or list directory (returns entries array)",
            params: { path: "query param (required) — file or directory path", encoding: "query param — file encoding (default: utf-8)" },
            responseExampleFile: { ok: true, data: { path: "/app/config.json", type: "file", size: 256, content: '{\"port\":3000}' } },
            responseExampleDir: { ok: true, data: { path: "/app", type: "dir", entries: [{ name: "server.js", type: "file", size: 1024 }] } },
          },
          {
            method: "POST", path: "/files",
            summary: "Write content to a file (creates or overwrites; auto-creates parent dirs)",
            params: { path: "string (required) — file path", content: "string (required) — file content", append: "boolean — append instead of overwrite (default: false)", encoding: "string — file encoding (default: utf-8)" },
            requestExample: { path: "/app/config.json", content: '{\"port\": 3000}' },
            responseExample: { ok: true, data: { path: "/app/config.json", size: 17, appended: false } },
          },
          {
            method: "POST", path: "/files/mkdir",
            summary: "Create a directory (supports recursive creation)",
            params: { path: "string (required) — directory path", recursive: "boolean — create parent dirs if needed (default: true)" },
            requestExample: { path: "/app/logs/2024" },
            responseExample: { ok: true, data: { path: "/app/logs/2024", created: true } },
          },
          {
            method: "DELETE", path: "/files?path=",
            summary: "Delete a file or directory (recursive, force)",
            params: { path: "query param (required) — path to delete" },
            responseExample: { ok: true, data: { path: "/app/old-dir", deleted: true } },
          },
          {
            method: "POST", path: "/files/move",
            summary: "Move or rename a file/directory",
            params: { from: "string (required) — source path", to: "string (required) — destination path" },
            requestExample: { from: "/app/old.json", to: "/app/config/new.json" },
            responseExample: { ok: true, data: { from: "/app/old.json", to: "/app/config/new.json", moved: true } },
          },
          {
            method: "GET", path: "/files/stat?path=",
            summary: "Get file metadata (size, timestamps, mode, type)",
            params: { path: "query param (required) — file path" },
            responseExample: { ok: true, data: { path: "/app/server.js", size: 1024, isFile: true, isDir: false, mode: "644", mtime: "2024-01-01T00:00:00Z", birthtime: "2024-01-01T00:00:00Z" } },
          },
          {
            method: "POST", path: "/files/search",
            summary: "Search file contents with regex (grep). Scans recursively, skips node_modules and dotfiles.",
            params: { pattern: "string (required) — regex pattern", path: "string — directory to search (default: .)", recursive: "boolean — search recursively (default: true)", maxResults: "number — max matches (default: 100)" },
            requestExample: { pattern: "TODO|FIXME", path: "./src", maxResults: 20 },
            responseExample: { ok: true, data: [{ file: "/app/src/index.js", line: 42, content: "// TODO: handle error" }], meta: { count: 1 } },
          },
        ],
      },
      {
        name: "Process Management",
        description: "List and kill processes running on the host.",
        endpoints: [
          {
            method: "GET", path: "/processes",
            summary: "List running processes (uses ps on Unix, tasklist on Windows)",
            params: { filter: "query param — filter keyword (e.g. 'node')" },
            responseExample: { ok: true, data: [{ user: "root", pid: 1234, cpu: "0.0", mem: "1.2", command: "node server.js" }], meta: { count: 1 } },
          },
          {
            method: "POST", path: "/processes/:pid/kill",
            summary: "Send a signal to a process by PID",
            params: { pid: "path param (required) — process ID", signal: "body field — signal name (default: SIGTERM)" },
            requestExample: { signal: "SIGTERM" },
            responseExample: { ok: true, data: { pid: 1234, signal: "SIGTERM", killed: true } },
          },
        ],
      },
      {
        name: "System Information",
        description: "Inspect host system: CPU, memory, disk, network, listening ports.",
        endpoints: [
          {
            method: "GET", path: "/system",
            summary: "Full system information: hostname, platform, arch, CPU count, memory, network interfaces, load average",
            responseExample: { ok: true, data: { hostname: "server-01", platform: "linux", arch: "x64", cpuCount: 4, totalMemory: 8589934592, freeMemory: 4294967296, network: { eth0: [{ address: "192.168.1.10", family: "IPv4" }] } } },
          },
          {
            method: "GET", path: "/system/ports",
            summary: "List all listening TCP ports with associated PIDs",
            responseExample: { ok: true, data: [{ port: 3000, pid: 1234 }, { port: 9100, pid: 5678 }], meta: { count: 2 } },
          },
          {
            method: "GET", path: "/system/disk",
            summary: "Disk usage information (uses df on Unix, wmic on Windows)",
            responseExample: { ok: true, data: { raw: "Filesystem  Size  Used  Avail  Use%  Mounted", parsed: [{ filesystem: "/dev/sda1", size: "50G", used: "20G", avail: "30G", usePercent: "40%", mounted: "/" }] } },
          },
        ],
      },
      {
        name: "Service Registry",
        description: "Register, query, update, and manage services deployed by the LLM. Services are persisted in data/services.json and survive server restarts.",
        standardRecord: {
          description: "Standard service record format stored in data/services.json",
          fields: {
            id: "string — auto-generated UUID (can be provided)",
            name: "string — human-readable service name",
            type: "web | api | database | cache | queue | worker | proxy | custom",
            port: "number | null — port the service listens on",
            pid: "number | null — process ID of the service",
            host: "string — hostname or IP (default: localhost)",
            status: "running | stopped | error | deploying | unknown",
            healthCheck: { type: "http | tcp | none", url: "string | null — health check URL", interval: "number — check interval in seconds" },
            tags: "string[] — searchable tags for filtering",
            config: "object — arbitrary configuration key-values",
            metadata: "object — arbitrary metadata key-values",
            workingDir: "string | null — working directory of the service",
            startCommand: "string | null — command to start the service",
            stopCommand: "string | null — command to stop the service",
            logPath: "string | null — path to log files",
            deployedAt: "ISO 8601 timestamp — when the service was registered",
            updatedAt: "ISO 8601 timestamp — last update time",
          },
        },
        endpoints: [
          {
            method: "POST", path: "/services",
            summary: "Register a new service. Returns the created record with generated ID and timestamps.",
            params: "Any subset of the standard record fields. Required: name. Recommended: type, port, status, startCommand.",
            requestExample: { name: "my-api", type: "api", port: 3000, status: "running", tags: ["production"], startCommand: "node server.js", workingDir: "/app", healthCheck: { type: "http", url: "http://localhost:3000/health" } },
            responseExample: { ok: true, data: { id: "uuid", name: "my-api", type: "api", port: 3000, status: "running", tags: ["production"], deployedAt: "2024-01-01T00:00:00Z", updatedAt: "2024-01-01T00:00:00Z" }, meta: { serviceId: "uuid" } },
          },
          {
            method: "GET", path: "/services",
            summary: "Query services with flexible filters. All params are optional query string params.",
            params: { name: "exact name match", type: "service type", status: "service status", tag: "tag name", port: "port number", pid: "process ID", host: "hostname", q: "free-text search across name, tags, and metadata" },
            responseExample: { ok: true, data: [{ id: "uuid", name: "my-api", type: "api", port: 3000, status: "running" }], meta: { count: 1, filters: { status: "running" } } },
          },
          {
            method: "GET", path: "/services/:id",
            summary: "Get a single service record by ID",
            responseExample: { ok: true, data: { id: "uuid", name: "my-api", type: "api", port: 3000, status: "running", deployedAt: "2024-01-01T00:00:00Z", updatedAt: "2024-01-01T00:00:00Z" } },
          },
          {
            method: "PUT", path: "/services/:id",
            summary: "Update a service record (partial update, merges fields)",
            params: "Any subset of standard record fields to update",
            requestExample: { status: "stopped", pid: null },
            responseExample: { ok: true, data: { id: "uuid", status: "stopped", updatedAt: "new-timestamp" } },
          },
          {
            method: "DELETE", path: "/services/:id",
            summary: "Delete a service record from the registry",
            responseExample: { ok: true, data: { deleted: true, id: "uuid" } },
          },
          {
            method: "POST", path: "/services/:id/start",
            summary: "Start a registered service. If startCommand is set, spawns it as a background task and links the PID.",
            params: { command: "body field — override the startCommand", cwd: "body field — override workingDir" },
            requestExample: {},
            responseExample: { ok: true, data: { id: "uuid", status: "running", pid: 12345, metadata: { taskId: "task-uuid" } } },
          },
          {
            method: "POST", path: "/services/:id/stop",
            summary: "Stop a registered service. Kills the PID (if set) and associated background task, then marks status as stopped.",
            params: { signal: "body field — signal name (default: SIGTERM)" },
            responseExample: { ok: true, data: { id: "uuid", status: "stopped", pid: null, killed: true } },
          },
          {
            method: "GET", path: "/services/:id/health",
            summary: "Run health checks on a service (PID alive, port open, HTTP health URL). Auto-updates status.",
            responseExample: { ok: true, data: { service: { id: "uuid", status: "running" }, checks: { pidAlive: true, portOpen: true, httpOk: { status: 200, ok: true } }, healthy: true } },
          },
          {
            method: "DELETE", path: "/services",
            summary: "Clear all service records from the registry",
            responseExample: { ok: true, data: { cleared: true, count: 5 } },
          },
        ],
      },
      {
        name: "Network Tools",
        description: "Port checking, HTTP proxy requests, and DNS lookups.",
        endpoints: [
          {
            method: "GET", path: "/net/port/:port",
            summary: "Check if a TCP port is open and accepting connections",
            params: { port: "path param (required) — port number", host: "query param — target host (default: localhost)" },
            responseExample: { ok: true, data: { host: "localhost", port: 3000, open: true } },
          },
          {
            method: "POST", path: "/net/http",
            summary: "Make an outbound HTTP request through the server (proxy). Useful for LLM to reach external services.",
            params: { url: "string (required) — target URL", method: "string — HTTP method (default: GET)", headers: "object — request headers", body: "string | object — request body", timeout: "number — timeout in ms (default: 10000)" },
            requestExample: { url: "https://api.github.com/repos/nodejs/node", method: "GET", headers: { Accept: "application/json" } },
            responseExample: { ok: true, data: { status: 200, statusText: "OK", headers: {}, body: '{"id":...}' } },
          },
          {
            method: "GET", path: "/net/dns?domain=",
            summary: "DNS lookup for a domain",
            params: { domain: "query param (required) — domain name to resolve" },
            responseExample: { ok: true, data: { domain: "example.com", addresses: [{ address: "93.184.216.34", family: 4 }] } },
          },
        ],
      },
      {
        name: "Environment Variables",
        description: "Read and set environment variables for the server process and spawned children.",
        endpoints: [
          {
            method: "GET", path: "/env",
            summary: "Get all environment variables, or a specific one with ?key=",
            params: { key: "query param — specific variable name" },
            responseExample: { ok: true, data: { PATH: "/usr/bin:/bin", HOME: "/root" } },
          },
          {
            method: "POST", path: "/env",
            summary: "Set an environment variable (affects this process and future spawned children)",
            params: { key: "string (required) — variable name", value: "string — variable value" },
            requestExample: { key: "NODE_ENV", value: "production" },
            responseExample: { ok: true, data: { key: "NODE_ENV", value: "production", set: true } },
          },
        ],
      },
      {
        name: "Batch Operations",
        description: "Execute multiple operations in a single request. Useful for LLM to chain commands atomically.",
        endpoints: [
          {
            method: "POST", path: "/batch",
            summary: "Execute a sequence of operations. Each operation runs in order; errors don't stop subsequent operations.",
            params: { operations: "array of { type, ...fields } — supported types: exec, spawn, read, write, mkdir" },
            requestExample: { operations: [{ type: "mkdir", path: "/app/data" }, { type: "write", path: "/app/data/config.json", content: "{}" }, { type: "exec", command: "ls -la /app/data" }] },
            responseExample: { ok: true, data: [{ ok: true, type: "mkdir", result: { created: true } }, { ok: true, type: "write", result: { written: true } }, { ok: true, type: "exec", result: { exitCode: 0, stdout: "total 8\ndrwxr-xr-x  3 root root 4096 config.json\n", stderr: "" } }] },
          },
        ],
      },
    ],
  };
}


// ── Utility Functions ──────────────────────────────────────────────────────

function checkPort(host, port) {
  return new Promise((resolve) => {
    
    const socket = new net.Socket();
    socket.setTimeout(2000);
    socket.on("connect", () => {
      socket.destroy();
      resolve(true);
    });
    socket.on("timeout", () => {
      socket.destroy();
      resolve(false);
    });
    socket.on("error", () => {
      resolve(false);
    });
    socket.connect(port, host);
  });
}

function execAsync(command, opts = {}) {
  return new Promise((resolve) => {
    exec(command, { ...opts, maxBuffer: 5 * 1024 * 1024, timeout: opts.timeout || 30000 }, (error, stdout, stderr) => {
      resolve({
        exitCode: error ? (typeof error.code === "number" ? error.code : -1) : 0,
        stdout,
        stderr,
        timedOut: error?.killed === true,
        ...(error ? { error: error.message } : {}),
      });
    });
  });
}

// ─── HTTP Server ───────────────────────────────────────────────────────────

const server = http.createServer(async (req, res) => {
  // Handle CORS preflight
  if (req.method === "OPTIONS") {
    sendJSON(res, ok({ cors: true }), 204);
    return;
  }

  const url = new URL(req.url, `http://${req.headers.host}`);
  const pathname = url.pathname;

  // Try to match a route
  const match = router.match(req.method, pathname);

  if (!match) {
    sendJSON(res, fail(`Not found: ${req.method} ${pathname}`, "NOT_FOUND", 404), 404);
    return;
  }

  try {
    await match.handler(req, res, match.params);
  } catch (err) {
    if (!res.headersSent) {
      const status = err.status || 500;
      const code = err.code || "INTERNAL_ERROR";
      sendJSON(res, fail(err.message, code, status, err.stack), status);
    }
  }
});

server.requestTimeout = 120_000;
server.headersTimeout = 15_000;
server.keepAliveTimeout = 5_000;

// Periodic cleanup of old completed tasks
setInterval(() => {
  taskManager.cleanup();
}, 300_000); // every 5 minutes

// Graceful shutdown
function shutdown(signal) {
  console.log(`\n[${signal}] Shutting down shell-ai-server...`);

  healthMonitor.stop();
  // Kill all running background tasks
  for (const [id, entry] of taskManager.tasks) {
    if (entry.task.status === "running" && entry.child) {
      try { entry.child.kill("SIGTERM"); } catch {}
    }
  }

  server.close(() => {
    console.log("Server closed.");
    process.exit(0);
  });

  // Force exit after 3s
  setTimeout(() => process.exit(1), 3000);
}

process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));

// Start
server.listen(PORT, HOST, () => {
  const nets = networkInterfaces();
  let lanIP = "localhost";
  for (const name of Object.keys(nets)) {
    for (const net of nets[name]) {
      if (net.family === "IPv4" && !net.internal) { lanIP = net.address; break; }
    }
    if (lanIP !== "localhost") break;
  }
  healthMonitor.start();
  console.log(`
Listening on http://${lanIP}:${PORT}  (0.0.0.0)
Api docs: http://${lanIP}:${PORT}/api
API JSON: http://${lanIP}:${PORT}/api
API summary:
  GET  /              — Server info
  GET  /api           — Full API docs (JSON)
  GET  /health        — Health check
  POST /shell/exec    — Execute command
  POST /shell/spawn   — Start background task
  GET  /shell/tasks   — List tasks
  POST /services      — Register service
  GET  /services      — Query services
  GET  /services/monitor/status — Health monitor
  GET  /system        — System info

Press Ctrl+C to stop.
`);
});
