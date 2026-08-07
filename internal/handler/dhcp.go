package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/dnsmasq"
	"github.com/joyops/infra-pxe/internal/store"
)

// --- DHCP types ---

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

type dhcpConfig struct {
	Interface   string `json:"interface"`
	DhcpStart   string `json:"dhcp_start"`
	DhcpEnd     string `json:"dhcp_end"`
	Netmask     string `json:"netmask"`
	Gateway     string `json:"gateway"`
	Dns         string `json:"dns"`
	LeaseTime   string `json:"lease_time"`
	EnableDns   bool   `json:"enable_dns"`
	Running     bool   `json:"running"`
	HostEntries int    `json:"host_entries"`
}

type dhcpConfigUpdate struct {
	Interface  string `json:"interface"`
	DhcpStart  string `json:"dhcp_start"`
	DhcpEnd    string `json:"dhcp_end"`
	Netmask    string `json:"netmask"`
	Gateway    string `json:"gateway"`
	Dns        string `json:"dns"`
	LeaseTime  string `json:"lease_time"`
	EnableDns  bool   `json:"enable_dns"`
}

// --- DHCP handlers ---

func dhcpBindingsListHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")
		bindings, err := parseHostsFile(hostsPath)
		if err != nil {
			// If file doesn't exist, return empty list
			if os.IsNotExist(err) {
				jsonOK(w, []dhcpBinding{})
				return
			}
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, bindings)
	}
}

func dhcpBindingsCreateHandler(cfg *config.Config, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b dhcpBinding
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if b.MAC == "" || b.IP == "" {
			jsonError(w, http.StatusBadRequest, "mac and ip are required")
			return
		}

		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")

		// Read existing entries to check for duplicate MAC
		existing, _ := parseHostsFile(hostsPath)
		for _, e := range existing {
			if strings.EqualFold(e.MAC, b.MAC) {
				jsonError(w, http.StatusConflict, "binding for this MAC already exists")
				return
			}
		}

		// Build line: mac,ip,hostname,lease_time
		line := fmt.Sprintf("%s,%s", b.MAC, b.IP)
		if b.Hostname != "" {
			line += "," + b.Hostname
		}
		line += "," + cfg.Dnsmasq.LeaseTime

		// Append to hosts file
		f, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()

		// Ensure we start on a new line
		if info, _ := f.Stat(); info.Size() > 0 {
			f.WriteString("\n")
		}
		f.WriteString(line + "\n")

		d.Reload()
		w.WriteHeader(http.StatusCreated)
		jsonOK(w, b)
	}
}

func dhcpBindingsDeleteHandler(cfg *config.Config, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mac := r.PathValue("mac")
		if mac == "" {
			jsonError(w, http.StatusBadRequest, "mac is required")
			return
		}

		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")
		data, err := os.ReadFile(hostsPath)
		if err != nil {
			if os.IsNotExist(err) {
				jsonError(w, http.StatusNotFound, "no bindings found")
				return
			}
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
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
			if len(parts) >= 2 && strings.EqualFold(parts[0], mac) {
				found = true
				continue
			}
			kept = append(kept, trimmed)
		}

		if !found {
			jsonError(w, http.StatusNotFound, "binding not found")
			return
		}

		content := ""
		if len(kept) > 0 {
			content = strings.Join(kept, "\n") + "\n"
		}
		if err := os.WriteFile(hostsPath, []byte(content), 0o644); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}

		d.Reload()
		jsonOK(w, map[string]string{"deleted": mac})
	}
}

func dhcpLeasesHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		leasesPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.leases")
		f, err := os.Open(leasesPath)
		if err != nil {
			if os.IsNotExist(err) {
				jsonOK(w, []dhcpLease{})
				return
			}
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()

		var leases []dhcpLease
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			// Format: timestamp mac ip hostname clientid
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
		jsonOK(w, leases)
	}
}
func dhcpConfigGetHandler(cfg *config.Config, s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostsPath := filepath.Join(cfg.DnsmasqConfDir(), "dnsmasq.hostsfile")
		hostEntries := 0
		if data, err := os.ReadFile(hostsPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) != "" {
					hostEntries++
				}
			}
		}
		dbCfg := s.DB.GetDhcpConfig()
		c := dhcpConfig{
			Interface:   dbCfg.Interface,
			DhcpStart:   dbCfg.DhcpStart,
			DhcpEnd:     dbCfg.DhcpEnd,
			Netmask:     dbCfg.Netmask,
			Gateway:     dbCfg.Gateway,
			Dns:         dbCfg.Dns,
			LeaseTime:   dbCfg.LeaseTime,
			EnableDns:   dbCfg.EnableDns,
			Running:     d.IsRunning(),
			HostEntries: hostEntries,
		}
		jsonOK(w, c)
	}
}

func dhcpConfigUpdateHandler(cfg *config.Config, s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var update dhcpConfigUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Persist to DB
		s.DB.SetDhcpConfig(&db.DhcpConfig{
			Interface: update.Interface,
			DhcpStart: update.DhcpStart,
			DhcpEnd:   update.DhcpEnd,
			Netmask:   update.Netmask,
			Gateway:   update.Gateway,
			Dns:       update.Dns,
			LeaseTime: update.LeaseTime,
			EnableDns: update.EnableDns,
		})

		// Also set pxe_server_ip from interface if not already set
		pxeIP, _ := s.DB.GetPxeServer()
		if pxeIP == "" || pxeIP == "127.0.0.1" {
			if ifIP := getInterfaceIP(update.Interface); ifIP != "" {
				s.DB.SetPxeServer(ifIP, strconv.Itoa(cfg.Server.Port))
			}
		}

		// Render and write dnsmasq.conf
		params := &dnsmasq.DhcpConfigParams{
			Interface: update.Interface,
			DhcpStart: update.DhcpStart,
			DhcpEnd:   update.DhcpEnd,
			Netmask:   update.Netmask,
			Gateway:   update.Gateway,
			Dns:       update.Dns,
			LeaseTime: update.LeaseTime,
			EnableDns: update.EnableDns,
		}
		d.WriteConfig(params)

		// Restart dnsmasq
		d.Stop()
		d.Start()

		jsonOK(w, dhcpConfig{
			Interface:   update.Interface,
			DhcpStart:   update.DhcpStart,
			DhcpEnd:     update.DhcpEnd,
			Netmask:     update.Netmask,
			Gateway:     update.Gateway,
			Dns:         update.Dns,
			LeaseTime:   update.LeaseTime,
			EnableDns:   update.EnableDns,
			Running:     d.IsRunning(),
			HostEntries: 0,
		})
	}
}

// --- helpers ---

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
		// Format: mac,ip,hostname,lease_time (hostname may be empty → mac,ip,,lease_time or mac,ip,lease_time)
		parts := strings.SplitN(trimmed, ",", 4)
		if len(parts) < 2 {
			continue
		}
		b := dhcpBinding{
			MAC: parts[0],
			IP:  parts[1],
		}
		if len(parts) >= 3 {
			// Could be hostname or lease_time — if it looks like a time (e.g. "5m"), skip
			if !looksLikeLeaseTime(parts[2]) {
				b.Hostname = parts[2]
			}
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
	// lease_time ends with s, m, h, d, or is "infinite"
	if s == "infinite" {
		return true
	}
	last := s[len(s)-1]
	return last == 's' || last == 'm' || last == 'h' || last == 'd'
}

// getInterfaceIP returns the first IPv4 address of a network interface.
func getInterfaceIP(ifName string) string {
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
