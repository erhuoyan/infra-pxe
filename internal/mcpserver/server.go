// Package mcpserver exposes PXE operations as MCP tools over Streamable HTTP.
package mcpserver

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/dnsmasq"
	"github.com/joyops/infra-pxe/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// NewHandler creates an http.Handler serving MCP tools over Streamable HTTP.
func NewHandler(cfg *config.Config, s *store.Store, d *dnsmasq.Manager) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "infra-runner",
		Version: "1.0.0",
	}, nil)

	registerTaskTools(server, s, d)
	registerDHCPTools(server, cfg, d)
	registerSystemTools(server, s, d)
	registerISOTools(server, cfg, s)
	registerOSTemplateTools(server, s)
	registerExtraTools(server, cfg, s, d)

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, nil)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Task tools
// ═══════════════════════════════════════════════════════════════════════════════

type ListTasksInput struct{}

func registerTaskTools(server *mcp.Server, s *store.Store, d *dnsmasq.Manager) {
	// list_tasks
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List all PXE deployment tasks",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *ListTasksInput) (*mcp.CallToolResult, any, error) {
		tasks, err := s.DB.ListTasks("")
		if err != nil {
			return nil, nil, fmt.Errorf("list tasks: %w", err)
		}
		return nil, tasks, nil
	})

	// get_task
	type GetTaskInput struct {
		SN string `json:"sn" jsonschema:"Serial number of the task to retrieve"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_task",
		Description: "Get a deployment task by serial number",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *GetTaskInput) (*mcp.CallToolResult, any, error) {
		if input.SN == "" {
			return nil, nil, fmt.Errorf("sn is required")
		}
		task, err := s.DB.GetTaskBySN(input.SN)
		if err != nil {
			return nil, nil, fmt.Errorf("task not found: %w", err)
		}
		return nil, task, nil
	})

	// create_task
	type CreateTaskInput struct {
		SN             string `json:"sn" jsonschema:"Serial number (required)"`
		Hostname       string `json:"hostname" jsonschema:"Target hostname (required)"`
		IP             string `json:"ip" jsonschema:"Target IP address (required)"`
		OS             string `json:"os" jsonschema:"OS template bid (required)"`
		RootPassword   string `json:"root_password" jsonschema:"Root password (default: CentOS@2026)"`
		DiskTargetSize int    `json:"disk_target_size" jsonschema:"Disk target size in GB (default: 480)"`
		Network        string `json:"network" jsonschema:"Network JSON (必填，必须有值，含 MAC: 单口 mac / bond slaves). 单口: {\"mac\":\"xx\",\"ip\":\"1.2.3.4\"}. Bond: {\"ip\":\"1.2.3.4\",\"bond\":{\"mode\":4,\"slaves\":[\"mac1\",\"mac2\"]}}"`
		Partition      string `json:"partition" jsonschema:"Partition config JSON string (default: {})"`
		SSHKeys        string `json:"ssh_keys" jsonschema:"SSH keys JSON array"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new PXE deployment task",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"sn":               map[string]any{"type": "string", "description": "Serial number (required)"},
				"hostname":         map[string]any{"type": "string", "description": "Target hostname (required)"},
				"ip":               map[string]any{"type": "string", "description": "Target IP address (required)"},
				"os":               map[string]any{"type": "string", "description": "OS template bid (required)"},
				"root_password":    map[string]any{"type": "string", "description": "Root password (default: CentOS@2026)"},
				"disk_target_size": map[string]any{"type": "integer", "description": "Disk target size in GB (default: 480)"},
				"network":          map[string]any{"type": "string", "description": "Network JSON (必填，含 MAC). 单口: {\"mac\":\"xx\",\"ip\":\"1.2.3.4\"}. Bond: {\"ip\":\"1.2.3.4\",\"bond\":{\"mode\":4,\"slaves\":[\"mac1\",\"mac2\"]}}"},
				"partition":        map[string]any{"type": "string", "description": "Partition config JSON string (default: {})"},
				"ssh_keys":         map[string]any{"type": "string", "description": "SSH keys JSON array"},
			},
			"required": []string{"sn", "hostname", "ip", "os", "network"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *CreateTaskInput) (*mcp.CallToolResult, any, error) {
		if input.SN == "" {
			return nil, nil, fmt.Errorf("sn is required")
		}
		// Check dnsmasq is alive
		if !d.IsRunning() {
			return nil, nil, fmt.Errorf("dnsmasq is not running — target machines cannot PXE boot. Start with dnsmasq_start or check for zombie process.")
		}
		// Validate OS template resources
		if input.OS != "" {
			v := s.ValidateOSTemplate(input.OS)
			if !v.Ready {
				return nil, nil, fmt.Errorf("os template '%s' not ready: %s", input.OS, formatValidationErrors(v))
			}
		}
		tc := &db.TaskCreate{
			SN:             input.SN,
			Hostname:       input.Hostname,
			IP:             input.IP,
			OS:             input.OS,
			RootPassword:   input.RootPassword,
			DiskTargetSize: input.DiskTargetSize,
			Network:        input.Network,
			Partition:      input.Partition,
			SSHKeys:        input.SSHKeys,
		}
		// Inherit scripts/files from os_template
		if input.OS != "" {
			s.InheritTemplateResources(tc)
		}
		task, err := s.DB.CreateTask(tc)
		if err != nil {
			return nil, nil, fmt.Errorf("create task: %w", err)
		}
		d.RegenerateConfig()
		return nil, task, nil
	})

	// delete_task
	type DeleteTaskInput struct {
		SN string `json:"sn" jsonschema:"Serial number of the task to delete"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_task",
		Description: "Delete a deployment task by serial number",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *DeleteTaskInput) (*mcp.CallToolResult, any, error) {
		if input.SN == "" {
			return nil, nil, fmt.Errorf("sn is required")
		}
		if err := s.DB.DeleteTask(input.SN); err != nil {
			return nil, nil, fmt.Errorf("delete task: %w", err)
		}
		d.RegenerateConfig()
		return nil, map[string]string{"deleted": input.SN}, nil
	})

	// list_task_history
	type ListHistoryInput struct {
		SN string `json:"sn" jsonschema:"Filter by serial number (optional, empty returns all)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_task_history",
		Description: "List completed/failed install history (tasks are removed after completion, history is preserved here)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"sn": map[string]any{"type": "string", "description": "Filter by serial number (optional, empty returns all)"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *ListHistoryInput) (*mcp.CallToolResult, any, error) {
		results, _ := s.DB.ListResults(input.SN, 50)
		return nil, results, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// DHCP tools
// ═══════════════════════════════════════════════════════════════════════════════

type dhcpBinding struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

type dhcpLease struct {
	Timestamp string `json:"timestamp"`
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	ClientID  string `json:"client_id"`
}

type NoInput struct{}

func registerDHCPTools(server *mcp.Server, cfg *config.Config, d *dnsmasq.Manager) {
	// list_dhcp_bindings
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_dhcp_bindings",
		Description: "List all static DHCP MAC-to-IP bindings",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")
		bindings, err := parseHostsFile(hostsPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, []dhcpBinding{}, nil
			}
			return nil, nil, fmt.Errorf("read bindings: %w", err)
		}
		return nil, bindings, nil
	})

	// create_dhcp_binding
	type CreateBindingInput struct {
		MAC      string `json:"mac" jsonschema:"MAC address (required)"`
		IP       string `json:"ip" jsonschema:"IP address to bind (required)"`
		Hostname string `json:"hostname" jsonschema:"Optional hostname for the binding"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_dhcp_binding",
		Description: "Create a static DHCP MAC-to-IP binding",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mac":      map[string]any{"type": "string", "description": "MAC address"},
				"ip":       map[string]any{"type": "string", "description": "IP address to bind"},
				"hostname": map[string]any{"type": "string", "description": "Optional hostname for the binding"},
			},
			"required": []string{"mac", "ip"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *CreateBindingInput) (*mcp.CallToolResult, any, error) {
		if input.MAC == "" || input.IP == "" {
			return nil, nil, fmt.Errorf("mac and ip are required")
		}

		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")

		// Check for duplicate MAC
		existing, _ := parseHostsFile(hostsPath)
		for _, e := range existing {
			if strings.EqualFold(e.MAC, input.MAC) {
				return nil, nil, fmt.Errorf("binding for MAC %s already exists", input.MAC)
			}
		}

		// Build line: mac,ip,hostname,lease_time
		line := fmt.Sprintf("%s,%s", input.MAC, input.IP)
		if input.Hostname != "" {
			line += "," + input.Hostname
		}
		line += "," + cfg.Dnsmasq.LeaseTime

		f, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open hosts file: %w", err)
		}
		defer f.Close()

		if info, _ := f.Stat(); info.Size() > 0 {
			f.WriteString("\n")
		}
		f.WriteString(line + "\n")

		d.Reload()
		return nil, dhcpBinding{MAC: input.MAC, IP: input.IP, Hostname: input.Hostname}, nil
	})

	// delete_dhcp_binding
	type DeleteBindingInput struct {
		MAC string `json:"mac" jsonschema:"MAC address of the binding to delete"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_dhcp_binding",
		Description: "Delete a static DHCP binding by MAC address",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *DeleteBindingInput) (*mcp.CallToolResult, any, error) {
		if input.MAC == "" {
			return nil, nil, fmt.Errorf("mac is required")
		}

		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")
		data, err := os.ReadFile(hostsPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("no bindings found")
			}
			return nil, nil, fmt.Errorf("read hosts file: %w", err)
		}

		lines := strings.Split(string(data), "\n")
		var kept []string
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			parts := strings.SplitN(trimmed, ",", 4)
			if len(parts) >= 2 && strings.EqualFold(parts[0], input.MAC) {
				found = true
				continue
			}
			kept = append(kept, trimmed)
		}

		if !found {
			return nil, nil, fmt.Errorf("binding not found for MAC %s", input.MAC)
		}

		content := ""
		if len(kept) > 0 {
			content = strings.Join(kept, "\n") + "\n"
		}
		if err := os.WriteFile(hostsPath, []byte(content), 0o644); err != nil {
			return nil, nil, fmt.Errorf("write hosts file: %w", err)
		}

		d.Reload()
		return nil, map[string]string{"deleted": input.MAC}, nil
	})

	// list_dhcp_leases
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_dhcp_leases",
		Description: "List current DHCP leases from dnsmasq",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		leasesPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.leases")
		f, err := os.Open(leasesPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, []dhcpLease{}, nil
			}
			return nil, nil, fmt.Errorf("read leases: %w", err)
		}
		defer f.Close()

		var leases []dhcpLease
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			leases = append(leases, dhcpLease{
				Timestamp: fields[0],
				MAC:       fields[1],
				IP:        fields[2],
				Hostname:  fields[3],
				ClientID:  fields[4],
			})
		}
		if leases == nil {
			leases = []dhcpLease{}
		}
		return nil, leases, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// System tools
// ═══════════════════════════════════════════════════════════════════════════════

func registerSystemTools(server *mcp.Server, s *store.Store, d *dnsmasq.Manager) {
	// system_status
	mcp.AddTool(server, &mcp.Tool{
		Name:        "system_status",
		Description: "Get PXE system status including dnsmasq state",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		status := map[string]any{
			"pxe":             "running",
			"dnsmasq":         d.Status(),
			"pending_results": s.PendingResultsCount(),
		}
		return nil, status, nil
	})

	// dnsmasq_start
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dnsmasq_start",
		Description: "Start the dnsmasq DHCP/TFTP service",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		if err := d.Start(); err != nil {
			return nil, nil, fmt.Errorf("start dnsmasq: %w", err)
		}
		return nil, map[string]string{"status": "started"}, nil
	})

	// dnsmasq_stop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dnsmasq_stop",
		Description: "Stop the dnsmasq DHCP/TFTP service",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		d.Stop()
		return nil, map[string]string{"status": "stopped"}, nil
	})

	// dnsmasq_reload
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dnsmasq_reload",
		Description: "Reload dnsmasq configuration (SIGHUP)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		d.Reload()
		return nil, map[string]string{"status": "reloaded"}, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// ISO tools
// ═══════════════════════════════════════════════════════════════════════════════

func registerISOTools(server *mcp.Server, cfg *config.Config, s *store.Store) {
	// list_iso
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_iso",
		Description: "List available ISO files with mount status",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		bootDir := cfg.BootDir()
		isoDir := filepath.Join(bootDir, "iso")

		var isos []map[string]any
		filepath.Walk(isoDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".iso") {
				return nil
			}
			rel, _ := filepath.Rel(isoDir, path)

			mounted := false
			mountedPath := ""
			if data, err := os.ReadFile("/proc/mounts"); err == nil {
				absIso, _ := filepath.EvalSymlinks(filepath.Join(isoDir, rel))
				for _, line := range strings.Split(string(data), "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 2 && fields[0] == absIso {
						mounted = true
						mountedPath = fields[1]
						break
					}
				}
			}

			fileSize := info.Size()
			if realInfo, err := os.Stat(path); err == nil {
				fileSize = realInfo.Size()
			}
			isos = append(isos, map[string]any{
				"filename":     info.Name(),
				"path":         rel,
				"size_mb":      fileSize / (1024 * 1024),
				"mounted":      mounted,
				"mounted_path": mountedPath,
			})
			return nil
		})
		if isos == nil {
			isos = []map[string]any{}
		}
		return nil, isos, nil
	})

	// mount_iso
	type MountISOInput struct {
		Bid        string `json:"bid" jsonschema:"OS template bid — auto-resolves filename and distro_path (preferred over manual)"`
		Filename   string `json:"filename" jsonschema:"ISO filename (used if bid not provided)"`
		DistroPath string `json:"distro_path" jsonschema:"Mount path (used if bid not provided)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mount_iso",
		Description: "Mount an ISO for PXE serving. Pass bid to auto-resolve from OS template, or filename+distro_path manually.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bid":         map[string]any{"type": "string", "description": "OS template bid — auto-resolves filename and distro_path (preferred)"},
				"filename":    map[string]any{"type": "string", "description": "ISO filename (used if bid not provided)"},
				"distro_path": map[string]any{"type": "string", "description": "Mount path (used if bid not provided)"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *MountISOInput) (*mcp.CallToolResult, any, error) {
		filename := input.Filename
		distroPath := input.DistroPath

		if input.Bid != "" {
			tpl := s.DB.FindOSTemplateByBid(input.Bid)
			if tpl == nil {
				return nil, nil, fmt.Errorf("os_template not found: %s", input.Bid)
			}
			if tpl.ISOPath != "" {
				filename = tpl.ISOPath
			}
			if tpl.DistroPath != "" {
				distroPath = tpl.DistroPath
			}
		}

		if filename == "" {
			return nil, nil, fmt.Errorf("filename is required (pass bid or filename)")
		}

		bootDir := cfg.BootDir()
		isoDir := filepath.Join(bootDir, "iso")
		httpDir := filepath.Join(bootDir, "http")
		isoPath := filepath.Join(isoDir, filename)

		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("ISO not found: %s", filename)
		}

		if distroPath == "" {
			distroPath = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
		}
		repoPath := filepath.Join(httpDir, distroPath, "repo")

		if isMountPoint(repoPath) {
			return nil, map[string]string{"status": "already_mounted", "path": repoPath}, nil
		}

		os.MkdirAll(repoPath, 0o755)
		cmd := exec.Command("mount", "-o", "loop,ro", isoPath, repoPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, nil, fmt.Errorf("mount failed: %s", strings.TrimSpace(string(output)))
		}

		return nil, map[string]any{
			"status":      "mounted",
			"iso":         input.Filename,
			"distro_path": distroPath,
			"repo_path":   repoPath,
		}, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// OS Template tools
// ═══════════════════════════════════════════════════════════════════════════════

func registerOSTemplateTools(server *mcp.Server, s *store.Store) {
	// list_os_templates
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_os_templates",
		Description: "List registered OS templates (available operating systems for PXE install)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		tpls, err := s.DB.ListOSTemplates()
		if err != nil {
			return nil, nil, fmt.Errorf("list os templates: %w", err)
		}
		return nil, tpls, nil
	})

	// validate_os_template
	type ValidateOSTemplateInput struct {
		Bid string `json:"bid" jsonschema:"OS template bid to validate (e.g. tpl-ubuntu2204-x64)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_os_template",
		Description: "Validate that all physical resources (ISO, mount, template, scripts, files) for an OS template are present and ready",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *ValidateOSTemplateInput) (*mcp.CallToolResult, any, error) {
		if input.Bid == "" {
			return nil, nil, fmt.Errorf("bid is required")
		}
		v := s.ValidateOSTemplate(input.Bid)
		return nil, v, nil
	})

	// update_os_template
	type UpdateOSTemplateInput struct {
		Bid        string  `json:"bid" jsonschema:"OS template bid to update (required)"`
		Label      *string `json:"label" jsonschema:"Display label"`
		ScriptBids *string `json:"script_bids" jsonschema:"Comma-separated script bids (e.g. scr-install-ofed,scr-setup-net)"`
		FileBids   *string `json:"file_bids" jsonschema:"Comma-separated file bids (e.g. fil-ofed58203-arm,fil-gpu-driver)"`
		KernelArgs *string `json:"kernel_args" jsonschema:"Kernel boot arguments"`
		MirrorURL  *string `json:"mirror_url" jsonschema:"Mirror URL for network install"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_os_template",
		Description: "Update an OS template's fields (label, script_bids, file_bids, kernel_args, mirror_url)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bid":         map[string]any{"type": "string", "description": "OS template bid to update"},
				"label":       map[string]any{"type": "string", "description": "Display label"},
				"script_bids": map[string]any{"type": "string", "description": "Comma-separated script bids (e.g. scr-install-ofed)"},
				"file_bids":   map[string]any{"type": "string", "description": "Comma-separated file bids (e.g. fil-ofed58203-arm)"},
				"kernel_args": map[string]any{"type": "string", "description": "Kernel boot arguments"},
				"mirror_url":  map[string]any{"type": "string", "description": "Mirror URL for network install"},
			},
			"required": []string{"bid"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *UpdateOSTemplateInput) (*mcp.CallToolResult, any, error) {
		if input.Bid == "" {
			return nil, nil, fmt.Errorf("bid is required")
		}
		tpl := s.DB.FindOSTemplateByBid(input.Bid)
		if tpl == nil {
			return nil, nil, fmt.Errorf("os_template not found: %s", input.Bid)
		}
		if input.Label != nil {
			tpl.Label = *input.Label
		}
		if input.ScriptBids != nil {
			tpl.ScriptBids = *input.ScriptBids
		}
		if input.FileBids != nil {
			tpl.FileBids = *input.FileBids
		}
		if input.KernelArgs != nil {
			tpl.KernelArgs = *input.KernelArgs
		}
		if input.MirrorURL != nil {
			tpl.MirrorURL = *input.MirrorURL
		}
		if err := s.DB.UpsertOSTemplate(tpl); err != nil {
			return nil, nil, fmt.Errorf("update os_template: %w", err)
		}
		return nil, tpl, nil
	})

	// list_scripts
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_scripts",
		Description: "List all registered post-install scripts",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		scripts, err := s.DB.ListScripts()
		if err != nil {
			return nil, nil, fmt.Errorf("list scripts: %w", err)
		}
		// Return without content for brevity
		type scriptSummary struct {
			Bid         string `json:"bid"`
			Name        string `json:"name"`
			ScriptType  string `json:"script_type"`
			Description string `json:"description"`
		}
		var summaries []scriptSummary
		for _, sc := range scripts {
			summaries = append(summaries, scriptSummary{
				Bid: sc.Bid, Name: sc.Name, ScriptType: sc.ScriptType, Description: sc.Description,
			})
		}
		return nil, summaries, nil
	})

	// create_script
	type CreateScriptInput struct {
		Bid         string  `json:"bid" jsonschema:"Script bid (e.g. scr-install-ofed)"`
		Name        string  `json:"name" jsonschema:"Script display name"`
		ScriptType  *string `json:"script_type" jsonschema:"Interpreter: bash, python, sh (default: bash)"`
		Description *string `json:"description" jsonschema:"Script description"`
		Content     string  `json:"content" jsonschema:"Script content (full source code)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_script",
		Description: "Create or update a post-install script (stored in DB, executed during OS install)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bid":         map[string]any{"type": "string", "description": "Script bid (e.g. scr-install-ofed)"},
				"name":        map[string]any{"type": "string", "description": "Script display name"},
				"script_type": map[string]any{"type": "string", "description": "Interpreter: bash, python, sh (default: bash)"},
				"description": map[string]any{"type": "string", "description": "Script description"},
				"content":     map[string]any{"type": "string", "description": "Script content (full source code)"},
			},
			"required": []string{"bid", "name", "content"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *CreateScriptInput) (*mcp.CallToolResult, any, error) {
		if input.Bid == "" || input.Content == "" {
			return nil, nil, fmt.Errorf("bid and content are required")
		}
		scriptType := "bash"
		if input.ScriptType != nil && *input.ScriptType != "" {
			scriptType = *input.ScriptType
		}
		desc := ""
		if input.Description != nil {
			desc = *input.Description
		}
		s.DB.UpsertScript(&db.Script{
			Bid:         input.Bid,
			Name:        input.Name,
			ScriptType:  scriptType,
			Description: desc,
			Content:     input.Content,
		})
		return nil, map[string]string{"bid": input.Bid, "status": "saved"}, nil
	})

	// list_files
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Description: "List all registered post-install files (metadata; physical files must exist on disk)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		files, err := s.DB.ListFiles()
		if err != nil {
			return nil, nil, fmt.Errorf("list files: %w", err)
		}
		return nil, files, nil
	})

	// create_file
	type CreateFileInput struct {
		Bid      string  `json:"bid" jsonschema:"File bid (e.g. fil-ofed58203-arm)"`
		Filename string  `json:"filename" jsonschema:"File name (e.g. MLNX_OFED_LINUX-5.8-2.0.3.0-openeuler22.03-aarch64.tar)"`
		DestDir  *string `json:"dest_dir" jsonschema:"Destination directory on target machine (default: /tmp/drivers)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_file",
		Description: "Register a post-install file (metadata). Physical file must be placed in boot/http/{bid}/ directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bid":      map[string]any{"type": "string", "description": "File bid (e.g. fil-ofed58203-arm)"},
				"filename": map[string]any{"type": "string", "description": "File name (e.g. MLNX_OFED_LINUX-5.8-2.0.3.0-openeuler22.03-aarch64.tar)"},
				"dest_dir": map[string]any{"type": "string", "description": "Destination directory on target machine (default: /tmp/drivers)"},
			},
			"required": []string{"bid", "filename"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *CreateFileInput) (*mcp.CallToolResult, any, error) {
		if input.Bid == "" || input.Filename == "" {
			return nil, nil, fmt.Errorf("bid and filename are required")
		}
		destDir := "/tmp/drivers"
		if input.DestDir != nil && *input.DestDir != "" {
			destDir = *input.DestDir
		}
		s.DB.UpsertFile(&db.File{
			Bid:      input.Bid,
			Filename: input.Filename,
			Path:     input.Bid + "/" + input.Filename,
			DestDir:  destDir,
		})
		return nil, map[string]string{"bid": input.Bid, "status": "saved"}, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Extra system tools (interfaces, dhcp config)
// ═══════════════════════════════════════════════════════════════════════════════

func registerExtraTools(server *mcp.Server, cfg *config.Config, s *store.Store, d *dnsmasq.Manager) {
	// list_interfaces
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_interfaces",
		Description: "List network interfaces with IPv4 addresses (for finding PXE interface IP)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		ifaces, err := net.Interfaces()
		if err != nil {
			return nil, nil, fmt.Errorf("list interfaces: %w", err)
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
		return nil, result, nil
	})

	// get_dhcp_config
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_dhcp_config",
		Description: "Get current DHCP/dnsmasq configuration (interface, range, lease time, running state)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *NoInput) (*mcp.CallToolResult, any, error) {
		dbCfg := s.DB.GetDhcpConfig()
		result := map[string]any{
			"interface":  dbCfg.Interface,
			"dhcp_start": dbCfg.DhcpStart,
			"dhcp_end":   dbCfg.DhcpEnd,
			"netmask":    dbCfg.Netmask,
			"gateway":    dbCfg.Gateway,
			"dns":        dbCfg.Dns,
			"lease_time": dbCfg.LeaseTime,
			"enable_dns": dbCfg.EnableDns,
			"running":    d.IsRunning(),
		}
		return nil, result, nil
	})

	// seed_import
	type SeedImportInput struct {
		Overwrite bool `json:"overwrite" jsonschema:"Force overwrite existing OS templates (default: false)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "seed_import",
		Description: "Import bundled seed data (OS templates from seeds/*.yaml). Idempotent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"overwrite": map[string]any{"type": "boolean", "description": "Force overwrite existing OS templates (default: false)"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *SeedImportInput) (*mcp.CallToolResult, any, error) {
		seedDir := filepath.Join(filepath.Dir(cfg.TemplatesDir()), "seeds")
		result := map[string]any{"seeds_dir": seedDir}

		// Import OS templates from seed yamls
		osResult, err := importSeedOSTemplates(s, seedDir, input.Overwrite)
		if err != nil {
			return nil, nil, fmt.Errorf("seed import os_templates: %w", err)
		}
		result["os_templates"] = osResult

		// Import scripts from seed yaml
		scriptsResult := importSeedScripts(s, seedDir, input.Overwrite)
		result["scripts"] = scriptsResult

		// Import files from seed yaml
		filesResult := importSeedFiles(s, seedDir, input.Overwrite)
		result["files"] = filesResult

		// Count templates on disk
		tplDir := cfg.TemplatesDir()
		tplCount := 0
		if entries, err := os.ReadDir(tplDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".j2") {
					tplCount++
				}
			}
		}
		if scEntries, err := os.ReadDir(filepath.Join(tplDir, "scripts")); err == nil {
			for _, e := range scEntries {
				if !e.IsDir() {
					tplCount++
				}
			}
		}
		result["templates_on_disk"] = tplCount

		return nil, result, nil
	})

	// dhcp_config_update
	type DhcpConfigUpdateInput struct {
		Interface string  `json:"interface" jsonschema:"Network interface for DHCP (e.g. br-pxe, eth0),required"`
		DhcpStart string  `json:"dhcp_start" jsonschema:"DHCP range start IP,required"`
		DhcpEnd   string  `json:"dhcp_end" jsonschema:"DHCP range end IP,required"`
		Netmask   string  `json:"netmask" jsonschema:"Subnet mask (e.g. 255.255.255.0),required"`
		Gateway   string  `json:"gateway" jsonschema:"Gateway IP,required"`
		Dns       *string `json:"dns" jsonschema:"DNS server IP (optional)"`
		LeaseTime *string `json:"lease_time" jsonschema:"DHCP lease time (e.g. 5m, 1h). Default: 5m"`
		EnableDns *bool   `json:"enable_dns" jsonschema:"Enable DNS forwarding (default: false)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dhcp_config_update",
		Description: "Configure DHCP settings (interface, IP range, gateway). Persists to DB and restarts dnsmasq.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"interface":  map[string]any{"type": "string", "description": "Network interface for DHCP (e.g. br-pxe, eth0)"},
				"dhcp_start": map[string]any{"type": "string", "description": "DHCP range start IP"},
				"dhcp_end":   map[string]any{"type": "string", "description": "DHCP range end IP"},
				"netmask":    map[string]any{"type": "string", "description": "Subnet mask (e.g. 255.255.255.0)"},
				"gateway":    map[string]any{"type": "string", "description": "Gateway IP"},
				"dns":        map[string]any{"type": "string", "description": "DNS server IP (optional)"},
				"lease_time": map[string]any{"type": "string", "description": "DHCP lease time (e.g. 5m, 1h). Default: 5m"},
				"enable_dns": map[string]any{"type": "boolean", "description": "Enable DNS forwarding (default: false)"},
			},
			"required": []string{"interface", "dhcp_start", "dhcp_end", "netmask", "gateway"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *DhcpConfigUpdateInput) (*mcp.CallToolResult, any, error) {
		if input.Interface == "" || input.DhcpStart == "" || input.DhcpEnd == "" {
			return nil, nil, fmt.Errorf("interface, dhcp_start, and dhcp_end are required")
		}
		if input.Netmask == "" {
			input.Netmask = "255.255.255.0"
		}
		leaseTime := "5m"
		if input.LeaseTime != nil && *input.LeaseTime != "" {
			leaseTime = *input.LeaseTime
		}
		dns := ""
		if input.Dns != nil {
			dns = *input.Dns
		}
		enableDns := false
		if input.EnableDns != nil {
			enableDns = *input.EnableDns
		}

		// Persist to DB
		s.DB.SetDhcpConfig(&db.DhcpConfig{
			Interface: input.Interface,
			DhcpStart: input.DhcpStart,
			DhcpEnd:   input.DhcpEnd,
			Netmask:   input.Netmask,
			Gateway:   input.Gateway,
			Dns:       dns,
			LeaseTime: leaseTime,
			EnableDns: enableDns,
		})

		// Auto-set pxe_server_ip from interface if not already set
		pxeIP, _ := s.DB.GetPxeServer()
		if pxeIP == "" || pxeIP == "127.0.0.1" {
			if ifIP := getIfaceIP(input.Interface); ifIP != "" {
				s.DB.SetPxeServer(ifIP, fmt.Sprintf("%d", cfg.Engine.Port))
			}
		}

		// Write dnsmasq.conf and restart
		d.WriteConfig(&dnsmasq.DhcpConfigParams{
			Interface: input.Interface,
			DhcpStart: input.DhcpStart,
			DhcpEnd:   input.DhcpEnd,
			Netmask:   input.Netmask,
			Gateway:   input.Gateway,
			Dns:       dns,
			LeaseTime: leaseTime,
			EnableDns: enableDns,
		})
		d.Stop()
		d.Start()

		return nil, map[string]any{
			"interface":  input.Interface,
			"dhcp_start": input.DhcpStart,
			"dhcp_end":   input.DhcpEnd,
			"netmask":    input.Netmask,
			"gateway":    input.Gateway,
			"running":    d.IsRunning(),
		}, nil
	})

	// iso_download
	type ISODownloadInput struct {
		URL      string `json:"url" jsonschema:"Download URL for the ISO file"`
		Filename string `json:"filename" jsonschema:"Target filename (optional, derived from URL if empty)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "iso_download",
		Description: "Download an ISO file from URL to the boot/iso/ directory (background, returns immediately)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":      map[string]any{"type": "string", "description": "Download URL for the ISO file"},
				"filename": map[string]any{"type": "string", "description": "Target filename (optional, derived from URL if empty)"},
			},
			"required": []string{"url"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *ISODownloadInput) (*mcp.CallToolResult, any, error) {
		if input.URL == "" {
			return nil, nil, fmt.Errorf("url is required")
		}
		filename := input.Filename
		if filename == "" {
			parts := strings.Split(input.URL, "/")
			filename = parts[len(parts)-1]
		}
		if filename == "" {
			return nil, nil, fmt.Errorf("cannot determine filename from URL")
		}

		isoDir := filepath.Join(cfg.BootDir(), "iso")
		destPath := filepath.Join(isoDir, filename)

		// Check if already exists
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			return nil, map[string]any{
				"status":   "already_exists",
				"filename": filename,
				"size_mb":  info.Size() / (1024 * 1024),
			}, nil
		}

		// Start background download via wget/curl
		os.MkdirAll(isoDir, 0o755)
		go func() {
			cmd := exec.Command("wget", "-q", "-O", destPath, input.URL)
			cmd.Run()
		}()

		return nil, map[string]any{
			"status":   "download_started",
			"filename": filename,
			"url":      input.URL,
			"dest":     destPath,
		}, nil
	})

	// template_push
	type TemplatePushInput struct {
		Name    string `json:"name" jsonschema:"Template filename (e.g. openeuler.ks.cfg.j2, scripts/pxe-pre.sh)"`
		Content string `json:"content" jsonschema:"Template file content"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "template_push",
		Description: "Create or update a kickstart/cloud-init template file",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input *TemplatePushInput) (*mcp.CallToolResult, any, error) {
		if input.Name == "" || input.Content == "" {
			return nil, nil, fmt.Errorf("name and content are required")
		}
		if strings.Contains(input.Name, "..") {
			return nil, nil, fmt.Errorf("path traversal not allowed")
		}
		p := filepath.Join(cfg.TemplatesDir(), input.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(input.Content), 0o644); err != nil {
			return nil, nil, fmt.Errorf("write template: %w", err)
		}
		return nil, map[string]any{"name": input.Name, "status": "saved"}, nil
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helpers (copied from handler/dhcp.go to avoid circular imports)
// ═══════════════════════════════════════════════════════════════════════════════

func parseHostsFile(path string) ([]dhcpBinding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var bindings []dhcpBinding
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ",", 4)
		if len(parts) < 2 {
			continue
		}
		b := dhcpBinding{
			MAC: parts[0],
			IP:  parts[1],
		}
		if len(parts) >= 3 && !looksLikeLeaseTime(parts[2]) {
			b.Hostname = parts[2]
		}
		bindings = append(bindings, b)
	}
	if bindings == nil {
		bindings = []dhcpBinding{}
	}
	return bindings, nil
}

func looksLikeLeaseTime(s string) bool {
	if s == "" {
		return false
	}
	if s == "infinite" {
		return true
	}
	last := s[len(s)-1]
	return last == 's' || last == 'm' || last == 'h' || last == 'd'
}

func isMountPoint(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == absPath {
			return true
		}
	}
	return false
}

func formatValidationErrors(v *store.OSTemplateValidation) string {
	var problems []string
	if v.Template.Status == "fail" {
		problems = append(problems, "template: "+v.Template.Detail)
	}
	if v.ISO.Status == "fail" {
		problems = append(problems, "iso: "+v.ISO.Detail)
	}
	if v.ISOMounted.Status == "fail" {
		problems = append(problems, "mount: "+v.ISOMounted.Detail)
	}
	if len(problems) == 0 {
		return "unknown error"
	}
	return strings.Join(problems, "; ")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Seed import helpers
// ═══════════════════════════════════════════════════════════════════════════════

type isoSourceRow struct {
	Bid          string `yaml:"bid"`
	DistroFamily string `yaml:"distro_family"`
	Version      string `yaml:"version"`
	Variant      string `yaml:"variant"`
	Arch         string `yaml:"arch"`
	ISOPath      string `yaml:"iso_path"`
}

type osTemplateRow struct {
	Bid        string `yaml:"bid"`
	Label      string `yaml:"label"`
	ISOBid     string `yaml:"iso_bid"`
	BootType   string `yaml:"boot_type"`
	Template   string `yaml:"template"`
	KernelArgs string `yaml:"kernel_args"`
	MirrorURL  string `yaml:"mirror_url"`
	ScriptBids string `yaml:"script_bids"`
	FileBids   string `yaml:"file_bids"`
}

func importSeedOSTemplates(s *store.Store, seedDir string, overwrite bool) (map[string]any, error) {
	result := map[string]any{"added": 0, "updated": 0, "skipped": 0}
	var errors []string

	isoData, err := os.ReadFile(filepath.Join(seedDir, "iso_sources.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read iso_sources.yaml: %w", err)
	}
	var isoRows []isoSourceRow
	if err := yaml.Unmarshal(isoData, &isoRows); err != nil {
		return nil, fmt.Errorf("parse iso_sources.yaml: %w", err)
	}

	osData, err := os.ReadFile(filepath.Join(seedDir, "os_templates.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read os_templates.yaml: %w", err)
	}
	var osRows []osTemplateRow
	if err := yaml.Unmarshal(osData, &osRows); err != nil {
		return nil, fmt.Errorf("parse os_templates.yaml: %w", err)
	}

	type isoInfo struct {
		DistroPath   string
		DistroFamily string
		ISOPath      string
	}
	isoMap := make(map[string]isoInfo, len(isoRows))
	for _, iso := range isoRows {
		variant := iso.Variant
		if variant == "" {
			variant = "standard"
		}
		isoMap[iso.Bid] = isoInfo{
			DistroPath:   fmt.Sprintf("%s/%s/%s/%s", iso.DistroFamily, iso.Version, variant, iso.Arch),
			DistroFamily: iso.DistroFamily,
			ISOPath:      iso.ISOPath,
		}
	}

	added, updated, skipped := 0, 0, 0
	for _, row := range osRows {
		if row.Bid == "" {
			errors = append(errors, "os_templates.yaml row missing bid")
			continue
		}
		info := isoMap[row.ISOBid]
		bootType := row.BootType
		if bootType == "" {
			bootType = "kickstart"
		}
		tpl := &db.OSTemplate{
			Bid:          row.Bid,
			Label:        row.Label,
			DistroPath:   info.DistroPath,
			DistroFamily: info.DistroFamily,
			BootType:     bootType,
			KernelArgs:   row.KernelArgs,
			Template:     row.Template,
			ISOPath:      info.ISOPath,
			MirrorURL:    row.MirrorURL,
			ScriptBids:   row.ScriptBids,
			FileBids:     row.FileBids,
		}

		existing, _ := s.DB.GetOSTemplate(row.Bid)
		if existing != nil && !overwrite {
			skipped++
			continue
		}
		if err := s.DB.UpsertOSTemplate(tpl); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", row.Bid, err))
			continue
		}
		if existing != nil {
			updated++
		} else {
			added++
		}
	}

	result["added"] = added
	result["updated"] = updated
	result["skipped"] = skipped
	if len(errors) > 0 {
		result["errors"] = errors
	}
	return result, nil
}

// getIfaceIP returns the first IPv4 address of a network interface.
func getIfaceIP(ifName string) string {
	if ifName == "" {
		return ""
	}
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

// --- Seed import: scripts ---

type scriptRow struct {
	Bid         string `yaml:"bid"`
	Name        string `yaml:"name"`
	ScriptType  string `yaml:"script_type"`
	Description string `yaml:"description"`
	Content     string `yaml:"content"`
}

func importSeedScripts(s *store.Store, seedDir string, overwrite bool) map[string]any {
	result := map[string]any{"added": 0, "skipped": 0}
	path := filepath.Join(seedDir, "scripts.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		// scripts.yaml is optional
		return result
	}
	var rows []scriptRow
	if err := yaml.Unmarshal(data, &rows); err != nil {
		result["error"] = err.Error()
		return result
	}

	added, skipped := 0, 0
	for _, row := range rows {
		if row.Bid == "" {
			continue
		}
		existing, _ := s.DB.GetScript(row.Bid)
		if existing != nil && !overwrite {
			skipped++
			continue
		}
		scriptType := row.ScriptType
		if scriptType == "" {
			scriptType = "bash"
		}
		s.DB.UpsertScript(&db.Script{
			Bid:         row.Bid,
			Name:        row.Name,
			ScriptType:  scriptType,
			Description: row.Description,
			Content:     row.Content,
		})
		added++
	}
	result["added"] = added
	result["skipped"] = skipped
	return result
}

// --- Seed import: files ---

type fileRow struct {
	Bid         string `yaml:"bid"`
	Name        string `yaml:"name"`
	Filename    string `yaml:"filename"`
	Category    string `yaml:"category"`
	DestDir     string `yaml:"dest_dir"`
	Description string `yaml:"description"`
}

func importSeedFiles(s *store.Store, seedDir string, overwrite bool) map[string]any {
	result := map[string]any{"added": 0, "skipped": 0}
	path := filepath.Join(seedDir, "files.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		// files.yaml is optional
		return result
	}
	var rows []fileRow
	if err := yaml.Unmarshal(data, &rows); err != nil {
		result["error"] = err.Error()
		return result
	}

	added, skipped := 0, 0
	for _, row := range rows {
		if row.Bid == "" {
			continue
		}
		existing, _ := s.DB.GetFile(row.Bid)
		if existing != nil && !overwrite {
			skipped++
			continue
		}
		destDir := row.DestDir
		if destDir == "" {
			destDir = "/tmp/drivers"
		}
		s.DB.UpsertFile(&db.File{
			Bid:      row.Bid,
			Filename: row.Filename,
			Path:     row.Bid + "/" + row.Filename,
			DestDir:  destDir,
		})
		added++
	}
	result["added"] = added
	result["skipped"] = skipped
	return result
}
