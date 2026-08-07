package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/dnsmasq"
	"github.com/joyops/infra-pxe/internal/mcpserver"
	"github.com/joyops/infra-pxe/internal/store"
)

// New creates the HTTP mux with all PXE endpoints.
func New(cfg *config.Config, s *store.Store, d *dnsmasq.Manager, cancelFunc context.CancelFunc) http.Handler {
	mux := http.NewServeMux()

	// ═══ Task CRUD API ═══════════════════════════════════
	mux.HandleFunc("POST /api/tasks", taskCreateHandler(s, d))
	mux.HandleFunc("GET /api/tasks", taskListHandler(s))
	mux.HandleFunc("GET /api/tasks/{sn}", taskGetHandler(s))
	mux.HandleFunc("PUT /api/tasks/{sn}", taskUpdateHandler(s, d))
	mux.HandleFunc("DELETE /api/tasks/{sn}", taskDeleteHandler(s, d))
	mux.HandleFunc("POST /api/tasks/batch", taskBatchCreateHandler(s, d))

	// ═══ OS Template CRUD ════════════════════════════════
	mux.HandleFunc("POST /api/os-templates", osTemplateUpsertHandler(s))
	mux.HandleFunc("GET /api/os-templates", osTemplateListHandler(s))
	mux.HandleFunc("GET /api/os-templates/{bid}", osTemplateGetHandler(s))
	mux.HandleFunc("DELETE /api/os-templates/{bid}", osTemplateDeleteHandler(s))

	// ═══ Template CRUD (kickstart/cloud-init) ════════════
	mux.HandleFunc("POST /api/templates", templateUpsertHandler(cfg, s))
	mux.HandleFunc("GET /api/templates", templateListHandler(cfg, s))
	mux.HandleFunc("GET /api/templates/{name...}", templateGetHandler(s))
	mux.HandleFunc("DELETE /api/templates/{name...}", templateDeleteHandler(cfg, s))

	// ═══ Seed import (bundled yaml → DB) ═════════════════
	mux.HandleFunc("POST /api/seed/import", seedImportHandler(cfg, s))

	// ═══ Results (history) ═══════════════════════════════
	mux.HandleFunc("GET /api/results", resultsListHandler(s))
	mux.HandleFunc("GET /api/results/{sn}", resultsBySNHandler(s))

	// ═══ Legacy sync (backward compat) ═══════════════════
	mux.HandleFunc("POST /api/sync", syncHandler(cfg, s, d))

	// ═══ System management ═══════════════════════════════
	mux.HandleFunc("GET /api/status", statusHandler(s, d))
	mux.HandleFunc("GET /api/system/status", statusHandler(s, d))
	mux.HandleFunc("POST /api/dnsmasq/start", dnsmasqStartHandler(d))
	mux.HandleFunc("POST /api/dnsmasq/stop", dnsmasqStopHandler(d))
	mux.HandleFunc("POST /api/dnsmasq/reload", dnsmasqReloadHandler(d))
	mux.HandleFunc("POST /api/shutdown", shutdownHandler(d, cancelFunc))
	mux.HandleFunc("GET /api/health", healthHandler())
	mux.HandleFunc("GET /api/interfaces", interfacesHandler())

	// ═══ Provision API (called by target servers) ════════
	mux.HandleFunc("GET /api/provision/by-sn/{sn}", provisionBySNHandler(s))
	mux.HandleFunc("GET /api/provision/by-mac/{mac}", provisionByMACHandler(s))
	mux.HandleFunc("POST /api/provision/complete", provisionCompleteHandler(cfg, s))
	mux.HandleFunc("POST /api/pxe/event", pxeEventHandler(cfg, s))
	mux.HandleFunc("POST /api/assets/{sn}/components", componentsHandler(cfg, s))
	mux.HandleFunc("POST /api/assets/{sn}/hardware", hardwareHandler(cfg, s))

	// ═══ PXE boot rendering ═════════════════════════════
	mux.HandleFunc("GET /render/menu.ipxe", menuHandler(cfg, s))
	mux.HandleFunc("GET /render/ks/{os_id}", kickstartHandler(cfg, s))
	mux.HandleFunc("GET /render/cloud-init/{os_id}/user-data", cloudInitUserDataHandler(cfg, s))
	mux.HandleFunc("GET /render/cloud-init/{os_id}/meta-data", cloudInitMetaDataHandler(cfg, s))
	mux.HandleFunc("GET /boot/{sn}", bootBySNHandler(cfg, s))
	mux.HandleFunc("GET /boot/mac/{mac}", bootByMACHandler(cfg, s))
	mux.HandleFunc("GET /api/pxe/scripts/{name...}", scriptHandler(cfg, s))

	// ═══ DHCP management ════════════════════════════════
	mux.HandleFunc("GET /api/dhcp/bindings", dhcpBindingsListHandler(cfg))
	mux.HandleFunc("POST /api/dhcp/bindings", dhcpBindingsCreateHandler(cfg, d))
	mux.HandleFunc("DELETE /api/dhcp/bindings/{mac}", dhcpBindingsDeleteHandler(cfg, d))
	mux.HandleFunc("GET /api/dhcp/leases", dhcpLeasesHandler(cfg))
	mux.HandleFunc("GET /api/dhcp/config", dhcpConfigGetHandler(cfg, s, d))
	mux.HandleFunc("PUT /api/dhcp/config", dhcpConfigUpdateHandler(cfg, s, d))

	// ═══ ISO management ═════════════════════════════════
	mux.HandleFunc("GET /api/iso/list", isoListHandler(cfg))
	mux.HandleFunc("POST /api/iso/mount", isoMountHandler(cfg))
	mux.HandleFunc("POST /api/iso/umount", isoUmountHandler(cfg))
	mux.HandleFunc("GET /api/iso/mounted", isoMountedHandler(cfg))
	mux.HandleFunc("POST /api/iso/download", isoDownloadHandler(cfg))

	// ═══ File management ════════════════════════════════
	mux.HandleFunc("GET /api/files", fileListHandler(cfg))
	mux.HandleFunc("GET /api/files/{bid}/check", fileCheckHandler(cfg))
	mux.HandleFunc("POST /api/files/upload", fileUploadHandler(cfg))
	mux.HandleFunc("POST /api/files/pull", filePullHandler(cfg))
	mux.HandleFunc("DELETE /api/files/{bid}", fileDeleteHandler(cfg))

	// ═══ MCP Server (Streamable HTTP) ═══════════════════
	mux.Handle("/mcp", mcpserver.NewHandler(cfg, s, d))

	// ═══ Static file serving ════════════════════════════
	bootDir := cfg.BootDir()
	httpDir := filepath.Join(bootDir, "http")
	mux.Handle("/", http.FileServer(http.Dir(httpDir)))
	mux.Handle("/tftp/", http.StripPrefix("/tftp/", http.FileServer(http.Dir(filepath.Join(bootDir, "tftp")))))
	mux.Handle("/iso/", http.StripPrefix("/iso/", http.FileServer(http.Dir(filepath.Join(bootDir, "iso")))))

	return loggingMiddleware(mux)
}

// loggingMiddleware logs every HTTP request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// shutdownHandler gracefully shuts down the PXE engine (stops dnsmasq + exits).
func shutdownHandler(d *dnsmasq.Manager, cancelFunc context.CancelFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]string{"status": "shutting_down"})
		go func() {
			time.Sleep(500 * time.Millisecond) // Let response flush
			d.Stop()
			cancelFunc()
			os.Exit(0) // Clean exit — systemd won't restart (Restart=on-failure)
		}()
	}
}

func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}
}

// --- JSON response helpers ---

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"code": "200", "message": "ok", "data": data,
	})
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"code": strconv.Itoa(code), "message": msg, "data": nil,
	})
}

// --- Handlers ---

func syncHandler(cfg *config.Config, s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Tasks          []json.RawMessage `json:"tasks"`
			OSRegistry     []json.RawMessage `json:"os_registry"`
			OSTemplates    []json.RawMessage `json:"os_templates"`
			PxeServerIP    string            `json:"pxe_server_ip"`
			PxeServerPort  string            `json:"pxe_server_port"`
			ServerHostname string            `json:"server_hostname"` // legacy compat
			Templates      map[string]string `json:"templates"`
			SyncVersion    int64             `json:"sync_version"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}

		// Compat: if old server_hostname is set but new fields empty, split it
		if payload.PxeServerIP == "" && payload.ServerHostname != "" {
			h, p, _ := net.SplitHostPort(payload.ServerHostname)
			payload.PxeServerIP = h
			payload.PxeServerPort = p
		}

		// Version check: skip if already applied
		if payload.SyncVersion > 0 && payload.SyncVersion <= s.GetSyncVersion() {
			jsonOK(w, map[string]any{"synced": 0, "skipped": true, "reason": "already_at_version"})
			return
		}

		count := s.SaveTasks(payload.Tasks, payload.PxeServerIP, payload.PxeServerPort)
		s.SaveOSTemplates(payload.OSTemplates)
		if len(payload.Templates) > 0 {
			s.SaveTemplates(payload.Templates)
		}
		if payload.SyncVersion > 0 {
			s.SetSyncVersion(payload.SyncVersion)
		}
		d.RegenerateConfig()
		jsonOK(w, map[string]any{"synced": count, "errors": []string{}})
	}
}

func statusHandler(s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{
			"pxe":             "running",
			"dnsmasq":         d.Status(),
			"pending_results": s.PendingResultsCount(),
		})
	}
}

func dnsmasqStartHandler(d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Start()
		jsonOK(w, map[string]string{"status": "started"})
	}
}

func dnsmasqStopHandler(d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Stop()
		jsonOK(w, map[string]string{"status": "stopped"})
	}
}

func dnsmasqReloadHandler(d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Reload()
		jsonOK(w, map[string]string{"status": "reloaded"})
	}
}

func provisionBySNHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		_, raw := s.GetTaskBySN(sn)
		if raw == nil {
			jsonError(w, 404, fmt.Sprintf("No task for SN %s", sn))
			return
		}
		// Return raw task JSON
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"code": "200", "message": "ok"}
		var data any
		json.Unmarshal(raw, &data)
		resp["data"] = data
		json.NewEncoder(w).Encode(resp)
	}
}

func provisionCompleteHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			SN  string `json:"sn"`
			Log string `json:"log"`
		}
		json.Unmarshal(body, &req)

		task, _ := s.GetTaskBySN(req.SN)
		var taskID *int
		if task != nil {
			taskID = &task.ID
		}

		result := store.Result{
			TaskID:      taskID,
			SN:          req.SN,
			Status:      "installed",
			InstallLog:  req.Log,
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		}
		s.SaveResult(result)

		// Forward to webhook (async, best-effort)
		forwardWebhook(cfg, "/api/provision/complete", body)

		// Archive locally — merge install log + status into {bid}.json (survives pusher cleanup)
		// NOTE: must resolve path BEFORE RemoveTaskBySN (otherwise bid lookup fails)
		archPath := archivePath(cfg, s, req.SN)
		mergeArchive(archPath, map[string]any{
			"sn":           req.SN,
			"status":       "installed",
			"install_log":  req.Log,
			"completed_at": result.CompletedAt,
		})

		s.RemoveTaskBySN(req.SN)

		jsonOK(w, map[string]any{"sn": req.SN, "status": "installed", "task_id": taskID})
	}
}

// archivePath returns the local archive path for a task (by bid or fallback SN).
func archivePath(cfg *config.Config, s *store.Store, sn string) string {
	dir := filepath.Join(cfg.BaseDir, "logs", "installs")
	os.MkdirAll(dir, 0755)
	name := sn
	if task, _ := s.GetTaskBySN(sn); task != nil && task.Bid != "" {
		name = task.Bid
	}
	return filepath.Join(dir, name+".json")
}

// mergeArchive reads existing archive JSON, merges new keys, writes back.
func mergeArchive(path string, merge map[string]any) {
	existing := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &existing)
	}
	for k, v := range merge {
		existing[k] = v
	}
	out, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(path, out, 0644)
}

func componentsHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		body, _ := io.ReadAll(r.Body)
		var components any
		json.Unmarshal(body, &components)

		result := store.Result{SN: sn, Status: "partial", Components: components}
		s.SaveResult(result)

		// Archive locally (survives pusher cleanup)
		mergeArchive(archivePath(cfg, s, sn), map[string]any{"sn": sn, "components": components})

		jsonOK(w, map[string]any{"sn": sn, "components_stored": true})
	}
}

func hardwareHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		body, _ := io.ReadAll(r.Body)
		var hw any
		json.Unmarshal(body, &hw)

		result := store.Result{SN: sn, Status: "partial", HardwareInfo: hw}
		s.SaveResult(result)

		// Archive locally
		mergeArchive(archivePath(cfg, s, sn), map[string]any{"sn": sn, "hardware": hw})

		jsonOK(w, map[string]any{"sn": sn, "hardware_stored": true})
	}
}

// pxeServerAddr returns the address target machines use to reach this PXE server.
// Priority: DB pxe_server_ip > DB dhcp_interface IP > 127.0.0.1
func pxeServerAddr(cfg *config.Config, s *store.Store) (ip, port string) {
	ip, port = s.GetPxeServer()
	if ip != "" && ip != "127.0.0.1" {
		return
	}
	// Fallback: get IP from DB-stored interface
	ifName := s.DB.GetInterface()
	if ifName != "" {
		if ifIP := getInterfaceIP(ifName); ifIP != "" {
			return ifIP, strconv.Itoa(cfg.Engine.Port)
		}
	}
	return "127.0.0.1", strconv.Itoa(cfg.Engine.Port)
}
func menuHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pxeIP, pxePort := pxeServerAddr(cfg, s)
		httpServer := pxeIP + ":" + pxePort
		osTpls := s.GetOSTemplates()

		// Group templates by distro family for menu display
		type menuEntry struct {
			ID         string
			Label      string
			DistroPath string
			BootType   string
			KernelArgs string
			ISOPath    string
		}
		groups := map[string][]menuEntry{}
		for _, t := range osTpls {
			groupLabel := strings.ToUpper(t.DistroFamily[:1]) + t.DistroFamily[1:] + " Linux"
			groups[groupLabel] = append(groups[groupLabel], menuEntry{
				ID:         "install_" + t.TplBid,
				Label:      t.Label,
				DistroPath: t.DistroPath,
				BootType:   t.BootType,
				KernelArgs: t.KernelArgs,
				ISOPath:    t.ISOPath,
			})
		}

		var sb strings.Builder
		sb.WriteString("#!ipxe\n\n")
		sb.WriteString(fmt.Sprintf("set http_server %s\n\n", httpServer))
		// Boot background: on EFI platforms lacking GOP/HII protocols this
		// silently fails (picture not shown) but must NOT wedge input. The
		// keyboard-freeze bug was caused by the USB HCD driver resetting the
		// BMC virtual keyboard bus, not by console --picture; keep the || guard.
		sb.WriteString("console --picture http://${http_server}/template/bootbg.png ||\n")
		sb.WriteString(":start\n")
		sb.WriteString("menu Welcome to iPXE Boot Menu\n\n")
		sb.WriteString("item --key a auto-install  [A] Auto Install\n")

		for groupLabel, entries := range groups {
			sb.WriteString(fmt.Sprintf("item --gap --             ------------------------- %s -------------------------\n", groupLabel))
			for _, e := range entries {
				sb.WriteString(fmt.Sprintf("item %s  %s\n", e.ID, e.Label))
			}
		}

		sb.WriteString("item --gap --             ------------------------- Other Options -------------------------\n")
		sb.WriteString("item --key l local        [L] Boot from local disk\n")
		sb.WriteString("item --key s shell        [S] iPXE Shell\n")
		sb.WriteString("item --key r reboot       [R] Reboot the Computer\n")
		sb.WriteString("item --key x exit         [X] Exit to BIOS\n\n")
		sb.WriteString("choose --default auto-install --timeout 10000 option && goto ${option} || goto start\n\n")

		// Auto-install: try SN first, then every NIC in turn.
		// ipxe.efi re-enumerates all adapters — the PXE-booting NIC is not
		// necessarily net0 on multi-port machines. Loop net0..netN via
		// nested setting expansion (${net${idx}/mac}); stop when idx is
		// beyond the last NIC (empty MAC).
		sb.WriteString(":auto-install\n")
		sb.WriteString("chain http://${http_server}/boot/${smbios/serial} ||\n")
		sb.WriteString("chain http://${http_server}/boot/mac/${netX/mac} ||\n")
		sb.WriteString("echo No active task found, booting from local disk...\n")
		sb.WriteString("sleep 3\n")
		sb.WriteString("goto local\n\n")

		// Per-OS boot entries
		for _, entries := range groups {
			for _, e := range entries {
				sb.WriteString(fmt.Sprintf(":%s\n", e.ID))
				sb.WriteString("imgfree\n")
				sb.WriteString(fmt.Sprintf("set base http://${http_server}/%s\n", e.DistroPath))
				if e.BootType == "kickstart" {
					sb.WriteString(fmt.Sprintf("kernel ${base}/repo/images/pxeboot/vmlinuz ip=dhcp inst.repo=${base}/repo inst.stage2=${base}/repo inst.ks=http://${http_server}/render/ks/%s %s || goto failed\n",
						e.ID[len("install_"):], e.KernelArgs))
					sb.WriteString("initrd ${base}/repo/images/pxeboot/initrd.img || goto failed\n")
				} else if e.BootType == "cloud-init" {
					isoURL := fmt.Sprintf("url=http://${http_server}/iso/%s", filepath.Base(e.ISOPath))
					sb.WriteString(fmt.Sprintf("kernel ${base}/repo/casper/vmlinuz root=/dev/ram0 ramdisk_size=4000000 autoinstall ip=dhcp ds=nocloud-net\\;s=http://${http_server}/render/cloud-init/%s/ %s cloud-config-url=/dev/null %s --- || goto failed\n",
						e.ID[len("install_"):], isoURL, e.KernelArgs))
					sb.WriteString("initrd ${base}/repo/casper/initrd || goto failed\n")
				}
				sb.WriteString("boot || goto failed\n")
				sb.WriteString("goto start\n\n")
			}
		}

		sb.WriteString(":local\nsanboot --no-describe --drive 0x80 || exit\n\n")
		sb.WriteString(":reboot\nreboot\n\n")
		sb.WriteString(":shell\nshell\n\n")
		sb.WriteString(":exit\nexit\n\n")
		sb.WriteString(":failed\necho Booting failed, dropping to shell\ngoto start\n")

		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(sb.String()))
	}
}

func kickstartHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		osID := r.PathValue("os_id")
		sn := r.URL.Query().Get("sn")
		if sn == "" {
			sn = r.URL.Query().Get("ks.sn")
		}

		pxeIP, pxePort := pxeServerAddr(cfg, s)

		// Find OS template for distro_path and distro_family
		osTpls := s.GetOSTemplates()
		var distroPath, distroFamily, tplName string
		for _, t := range osTpls {
			if t.TplBid == osID {
				distroPath = t.DistroPath
				distroFamily = t.DistroFamily
				tplName = t.Template
				break
			}
		}
		if distroFamily == "" {
			if strings.Contains(osID, "openeuler") {
				distroFamily = "openeuler"
			} else {
				distroFamily = "centos"
			}
		}

		// Get disk_target_size from task if SN provided
		diskTargetSize := "480"
		if sn != "" {
			if task, _ := s.GetTaskBySN(sn); task != nil {
				if task.DiskTargetSize > 0 {
					diskTargetSize = strconv.Itoa(task.DiskTargetSize)
				}
			}
		}

		// Try to load template: specific template name → {os_id}.ks.cfg.j2 → ks.cfg.j2
		var tplContent string
		var ok bool
		if tplName != "" {
			tplContent, ok = s.GetTemplate(tplName)
		}
		if !ok {
			tplContent, ok = s.GetTemplate(osID + ".ks.cfg.j2")
		}
		if !ok {
			tplContent, ok = s.GetTemplate("ks.cfg.j2")
		}
		if !ok {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "# Kickstart for %s — no template available\n", osID)
			return
		}

		vars := map[string]string{
			"pxe_server_ip":    pxeIP,
			"pxe_server_port":  pxePort,
			"distro_path":      distroPath,
			"disk_target_size": diskTargetSize,
			"distro_family":    distroFamily,
		}

		rendered := renderTemplate(tplContent, vars)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(rendered))
	}
}

func cloudInitUserDataHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		osID := r.PathValue("os_id")
		pxeIP, pxePort := pxeServerAddr(cfg, s)

		// Find OS template
		osTpls := s.GetOSTemplates()
		var distroPath, tplName, mirrorURL string
		for _, t := range osTpls {
			if t.TplBid == osID {
				distroPath = t.DistroPath
				tplName = t.Template
				mirrorURL = t.MirrorURL
				break
			}
		}

		// Load template: specific name → user-data.j2
		var tplContent string
		var ok bool
		if tplName != "" {
			tplContent, ok = s.GetTemplate(tplName)
		}
		if !ok {
			tplContent, ok = s.GetTemplate("user-data.j2")
		}
		if !ok {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "# cloud-init user-data for %s — no template available\n", osID)
			return
		}

		// If no mirror_url, default to local ISO repo
		if mirrorURL == "" {
			mirrorURL = fmt.Sprintf("http://%s:%s/%s/repo", pxeIP, pxePort, distroPath)
		}

		vars := map[string]string{
			"pxe_server_ip":   pxeIP,
			"pxe_server_port": pxePort,
			"distro_path":     distroPath,
			"mirror_url":      mirrorURL,
		}

		rendered := renderTemplate(tplContent, vars)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(rendered))
	}
}

func cloudInitMetaDataHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(""))
	}
}

func bootBySNHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		task, _ := s.GetTaskBySN(sn)
		if task == nil {
			jsonError(w, 404, fmt.Sprintf("No task for SN %s", sn))
			return
		}
		pxeIP, pxePort := pxeServerAddr(cfg, s)

		// Find OS template
		osTpls := s.GetOSTemplates()
		var tpl *store.OSTemplate
		for i := range osTpls {
			if osTpls[i].TplBid == task.OSID {
				tpl = &osTpls[i]
				break
			}
		}

		baseURL := fmt.Sprintf("http://%s:%s", pxeIP, pxePort)
		w.Header().Set("Content-Type", "text/plain")

		if tpl == nil {
			fmt.Fprintf(w, "#!ipxe\necho No OS template found for %s\nshell\n", task.OSID)
			return
		}

		fmt.Fprintf(w, "#!ipxe\nimgfree\nset base %s/%s\n", baseURL, tpl.DistroPath)
		if tpl.BootType == "kickstart" {
			fmt.Fprintf(w, "kernel ${base}/repo/images/pxeboot/vmlinuz ip=dhcp inst.repo=${base}/repo inst.stage2=${base}/repo inst.ks=%s/render/ks/%s?sn=%s %s || goto failed\n",
				baseURL, tpl.TplBid, sn, tpl.KernelArgs)
			fmt.Fprint(w, "initrd ${base}/repo/images/pxeboot/initrd.img || goto failed\n")
		} else if tpl.BootType == "cloud-init" {
			isoURL := fmt.Sprintf("url=%s/iso/%s", baseURL, filepath.Base(tpl.ISOPath))
			seedURL := fmt.Sprintf("%s/render/cloud-init/%s/", baseURL, tpl.TplBid)
			fmt.Fprint(w, "kernel ${base}/repo/casper/vmlinuz || goto failed\n")
			fmt.Fprint(w, "initrd ${base}/repo/casper/initrd || goto failed\n")
			fmt.Fprintf(w, "imgargs vmlinuz initrd=initrd root=/dev/ram0 ramdisk_size=4000000 autoinstall ip=dhcp ds=nocloud-net;s=%s %s cloud-config-url=/dev/null %s --- || goto failed\n",
				seedURL, isoURL, tpl.KernelArgs)
		}
		fmt.Fprint(w, "boot || goto failed\n")
		fmt.Fprintf(w, ":failed\necho Boot failed for SN %s\nshell\n", sn)
	}
}

func bootByMACHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mac := r.PathValue("mac")
		task := s.GetTaskByMAC(mac)
		if task == nil {
			http.Error(w, fmt.Sprintf("No task for MAC %s", mac), http.StatusNotFound)
			return
		}
		pxeIP, pxePort := pxeServerAddr(cfg, s)

		osTpls := s.GetOSTemplates()
		var tpl *store.OSTemplate
		for i := range osTpls {
			if osTpls[i].TplBid == task.OSID {
				tpl = &osTpls[i]
				break
			}
		}

		baseURL := fmt.Sprintf("http://%s:%s", pxeIP, pxePort)
		w.Header().Set("Content-Type", "text/plain")

		if tpl == nil {
			fmt.Fprintf(w, "#!ipxe\necho No OS template found for %s\nshell\n", task.OSID)
			return
		}

		fmt.Fprintf(w, "#!ipxe\nimgfree\nset base %s/%s\n", baseURL, tpl.DistroPath)
		if tpl.BootType == "kickstart" {
			fmt.Fprintf(w, "kernel ${base}/repo/images/pxeboot/vmlinuz ip=dhcp inst.repo=${base}/repo inst.stage2=${base}/repo inst.ks=%s/render/ks/%s?sn=%s %s || goto failed\n",
				baseURL, tpl.TplBid, task.SN, tpl.KernelArgs)
			fmt.Fprint(w, "initrd ${base}/repo/images/pxeboot/initrd.img || goto failed\n")
		} else if tpl.BootType == "cloud-init" {
			isoURL := fmt.Sprintf("url=%s/iso/%s", baseURL, filepath.Base(tpl.ISOPath))
			seedURL := fmt.Sprintf("%s/render/cloud-init/%s/", baseURL, tpl.TplBid)
			fmt.Fprint(w, "kernel ${base}/repo/casper/vmlinuz || goto failed\n")
			fmt.Fprint(w, "initrd ${base}/repo/casper/initrd || goto failed\n")
			fmt.Fprintf(w, "imgargs vmlinuz initrd=initrd root=/dev/ram0 ramdisk_size=4000000 autoinstall ip=dhcp ds=nocloud-net;s=%s %s cloud-config-url=/dev/null %s --- || goto failed\n",
				seedURL, isoURL, tpl.KernelArgs)
		}
		fmt.Fprint(w, "boot || goto failed\n")
		fmt.Fprintf(w, ":failed\necho Boot failed for SN %s (MAC %s)\nshell\n", task.SN, mac)
	}
}

func scriptHandler(_ *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if content, ok := s.GetTemplate("scripts/" + name); ok {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(content))
			return
		}
		jsonError(w, 404, fmt.Sprintf("Script not found: %s", name))
	}
}

func provisionByMACHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mac := r.PathValue("mac")
		task := s.GetTaskByMAC(mac)
		if task == nil {
			jsonError(w, 404, fmt.Sprintf("No task for MAC %s", mac))
			return
		}
		raw, _ := json.Marshal(task)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"code": "200", "message": "ok"}
		var data any
		json.Unmarshal(raw, &data)
		resp["data"] = data
		json.NewEncoder(w).Encode(resp)
	}
}

// stageToStatus maps pxe event stages to task status.
var stageToStatus = map[string]string{
	"sn_identifying":      "installing",
	"provision_matching":  "installing",
	"disk_selected":       "installing",
	"pkg_installing":      "installing",
	"post_start":          "configured",
	"network_configured":  "configured",
	"ssh_keys_injected":   "configured",
	"files_injecting":     "configured",
	"script_executing":    "configured",
	"hardware_collecting": "configured",
	"install_completing":  "configured",
	"provision_failed":    "failed",
}

func pxeEventHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var event struct {
			SN     string `json:"sn"`
			Stage  string `json:"stage"`
			Detail string `json:"detail"`
		}
		json.Unmarshal(body, &event)

		result := store.Result{
			SN:          event.SN,
			Status:      "event:" + event.Stage,
			InstallLog:  event.Detail,
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		}
		s.SaveResult(result)

		// Update task status based on stage
		if newStatus, ok := stageToStatus[event.Stage]; ok && event.SN != "" {
			s.DB.UpdateTask(event.SN, map[string]any{"status": newStatus})
		}

		// Forward to webhook (async, best-effort)
		forwardWebhook(cfg, "/api/pxe/event", body)

		jsonOK(w, map[string]any{"sn": event.SN, "stage": event.Stage, "received": true})
	}
}

// forwardWebhook relays a request body to the configured webhook URL if set.
// Async, best-effort — failure is silent (data is safe in local DB).
func forwardWebhook(cfg *config.Config, path string, body []byte) {
	if cfg.Webhook.URL == "" {
		return
	}
	go func() {
		url := strings.TrimRight(cfg.Webhook.URL, "/") + path
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.Webhook.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Webhook.Token)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
}

// renderTemplate performs simple Jinja2-style variable substitution and conditional blocks.
// Replaces {{ var }} with values from vars map, and handles {% if var != "value" %}...{% endif %} blocks.
var reVar = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
var reIfBlock = regexp.MustCompile(`(?s)\{%\s*if\s+(\w+)\s*!=\s*"([^"]+)"\s*%\}(.*?)\{%\s*endif\s*%\}`)
var reIfEqBlock = regexp.MustCompile(`(?s)\{%\s*if\s+(\w+)\s*==\s*"([^"]+)"\s*%\}(.*?)\{%\s*endif\s*%\}`)

func renderTemplate(content string, vars map[string]string) string {
	// Handle {% if var != "value" %}...{% endif %} blocks
	result := reIfBlock.ReplaceAllStringFunc(content, func(match string) string {
		parts := reIfBlock.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		varName, compareVal, body := parts[1], parts[2], parts[3]
		actual := vars[varName]
		if actual != compareVal {
			return body
		}
		return ""
	})

	// Handle {% if var == "value" %}...{% endif %} blocks
	result = reIfEqBlock.ReplaceAllStringFunc(result, func(match string) string {
		parts := reIfEqBlock.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		varName, compareVal, body := parts[1], parts[2], parts[3]
		actual := vars[varName]
		if actual == compareVal {
			return body
		}
		return ""
	})

	// Replace {{ var }} with values
	result = reVar.ReplaceAllStringFunc(result, func(match string) string {
		parts := reVar.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		if val, ok := vars[parts[1]]; ok {
			return val
		}
		return match
	})

	return result
}

// interfacesHandler returns local network interfaces (name + IPv4).
// GET /api/interfaces
func interfacesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ifaces, err := net.Interfaces()
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		type ifaceInfo struct {
			Name string   `json:"name"`
			IPs  []string `json:"ips"`
		}
		var result []ifaceInfo
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			var ips []string
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
					ips = append(ips, ipnet.IP.String())
				}
			}
			if len(ips) > 0 {
				result = append(result, ifaceInfo{Name: iface.Name, IPs: ips})
			}
		}
		if result == nil {
			result = []ifaceInfo{}
		}
		jsonOK(w, result)
	}
}
