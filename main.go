package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

var (
	registry      *ServiceRegistry
	healthMonitor *HealthMonitor
	taskManager   *TaskManager
)

const (
	defaultPort = 9100
	defaultHost = "0.0.0.0"
)

func main() {
	port := defaultPort
	if p := os.Getenv("PORT"); p != "" {
		if n, err := parseInt(p); err == nil && n > 0 {
			port = n
		}
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = defaultHost
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "shell-ai-server")
	}
	os.MkdirAll(dataDir, 0755)

	servicesFile := filepath.Join(dataDir, "services.json")
	logDir := filepath.Join(dataDir, "logs")

	registry = NewServiceRegistry(servicesFile)
	taskManager = NewTaskManager(logDir)
	healthMonitor = NewHealthMonitor(registry, taskManager)

	router := NewRouter()
	registerRoutes(router)

	handler := corsMiddleware(routerHandler(router))

	srv := &http.Server{
		Addr:              host + ":" + itoa(port),
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start health monitoring after all globals are initialised.
	healthMonitor.Start()

	// Periodically clean up completed background tasks.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			taskManager.Cleanup(time.Hour)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Println("[signal] Shutting down shell-ai-server...")
		healthMonitor.Stop()
		taskManager.Shutdown()
		ctx, cancel := contextWithTimeout(3 * time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Println("Server forced to close:", err)
		}
		os.Exit(0)
	}()

	log.Printf("shell-ai-server listening on %s (arch=%s, pid=%d, dataDir=%s)", srv.Addr, runtime.GOARCH, os.Getpid(), dataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// registerRoutes wires every HTTP endpoint to its handler.
func registerRoutes(rt *Router) {
	// Root and health.
	rt.Handle("GET", "/", handleRoot)
	rt.Handle("GET", "/health", handleHealth)
	rt.Handle("GET", "/api", handleAPIDocs)

	// Shell execution.
	rt.Handle("POST", "/shell/exec", handleShellExec)
	rt.Handle("POST", "/shell/spawn", handleShellSpawn)
	rt.Handle("GET", "/shell/tasks", handleShellTasks)
	rt.Handle("GET", "/shell/tasks/:id", wrapper(handleShellTaskGet))
	rt.Handle("GET", "/shell/tasks/:id/log", wrapper(handleShellTaskLog))
	rt.Handle("POST", "/shell/tasks/:id/kill", wrapper(handleShellTaskKill))
	rt.Handle("DELETE", "/shell/tasks/:id", wrapper(handleShellTaskDelete))
	rt.Handle("POST", "/shell/script", handleShellScript)

	// File operations.
	rt.Handle("GET", "/files", handleFilesGet)
	rt.Handle("POST", "/files", handleFilesWrite)
	rt.Handle("POST", "/files/mkdir", handleFilesMkdir)
	rt.Handle("DELETE", "/files", handleFilesDelete)
	rt.Handle("POST", "/files/move", handleFilesMove)
	rt.Handle("GET", "/files/stat", handleFilesStat)
	rt.Handle("POST", "/files/search", handleFilesSearch)

	// Processes.
	rt.Handle("GET", "/processes", handleProcesses)
	rt.Handle("POST", "/processes/:pid/kill", wrapper(handleProcessKill))

	// System.
	rt.Handle("GET", "/system", handleSystem)
	rt.Handle("GET", "/system/ports", handleSystemPorts)
	rt.Handle("GET", "/system/disk", handleSystemDisk)

	// Services.
	rt.Handle("POST", "/services", handleServiceRegister)
	rt.Handle("GET", "/services", handleServiceQuery)
	rt.Handle("GET", "/services/monitor/status", handleServiceMonitorStatus)
	rt.Handle("GET", "/services/:id", wrapper(handleServiceGet))
	rt.Handle("PUT", "/services/:id", wrapper(handleServiceUpdate))
	rt.Handle("DELETE", "/services/:id", wrapper(handleServiceDelete))
	rt.Handle("POST", "/services/:id/start", wrapper(handleServiceStart))
	rt.Handle("POST", "/services/:id/stop", wrapper(handleServiceStop))
	rt.Handle("GET", "/services/:id/health", wrapper(handleServiceHealth))
	rt.Handle("GET", "/services/:id/health-history", wrapper(handleServiceHealthHistory))
	rt.Handle("DELETE", "/services", handleServiceClear)

	// Network.
	rt.Handle("GET", "/net/port/:port", wrapper(handleNetPort))
	rt.Handle("POST", "/net/http", handleNetHTTP)
	rt.Handle("GET", "/net/dns", handleNetDNS)

	// Environment.
	rt.Handle("GET", "/env", handleEnvGet)
	rt.Handle("POST", "/env", handleEnvSet)

	// Batch.
	rt.Handle("POST", "/batch", handleBatch)
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, 200, ok(map[string]interface{}{
		"name":        "shell-ai-server",
		"version":     "1.0.0",
		"description": "A shell server for LLM clients",
		"endpoints":   getApiSummary(),
		"dataDir":     os.Getenv("DATA_DIR"),
		"uptime":      systemUptime(),
	}, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, 200, ok(map[string]interface{}{"status": "healthy", "uptime": systemUptime()}, nil))
}
