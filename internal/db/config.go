package db

// GetConfig retrieves a config value by key.
func (db *DB) GetConfig(key string) string {
	var value string
	db.conn.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	return value
}

// SetConfig sets a config key-value pair.
func (db *DB) SetConfig(key, value string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, key, value)
		return err
	})
}

// GetPxeServer returns the PXE server IP and port from config.
func (db *DB) GetPxeServer() (ip, port string) {
	ip = db.GetConfig("pxe_server_ip")
	port = db.GetConfig("pxe_server_port")
	return
}

// SetPxeServer stores the PXE server address in config.
func (db *DB) SetPxeServer(ip, port string) {
	db.SetConfig("pxe_server_ip", ip)
	db.SetConfig("pxe_server_port", port)
}

// GetSyncVersion returns the last applied sync version.
func (db *DB) GetSyncVersion() int64 {
	var v string
	db.conn.QueryRow(`SELECT value FROM config WHERE key = 'sync_version'`).Scan(&v)
	if v == "" {
		return 0
	}
	var n int64
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}

// SetSyncVersion stores the sync version.
func (db *DB) SetSyncVersion(version int64) {
	db.SetConfig("sync_version", itoa64(version))
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte(n%10) + '0'
		n /= 10
	}
	return string(buf[i:])
}

// --- DHCP configuration (persisted to DB KV store) ---

// DhcpConfig holds all DHCP/network parameters for dnsmasq.
type DhcpConfig struct {
	Interface string `json:"interface"`
	DhcpStart string `json:"dhcp_start"`
	DhcpEnd   string `json:"dhcp_end"`
	Netmask   string `json:"netmask"`
	Gateway   string `json:"gateway"`
	Dns       string `json:"dns"`
	LeaseTime string `json:"lease_time"`
	EnableDns bool   `json:"enable_dns"`
}

// GetDhcpConfig reads DHCP config from the KV store.
func (db *DB) GetDhcpConfig() *DhcpConfig {
	return &DhcpConfig{
		Interface: db.GetConfig("dhcp_interface"),
		DhcpStart: db.GetConfig("dhcp_start"),
		DhcpEnd:   db.GetConfig("dhcp_end"),
		Netmask:   db.GetConfig("dhcp_netmask"),
		Gateway:   db.GetConfig("dhcp_gateway"),
		Dns:       db.GetConfig("dhcp_dns"),
		LeaseTime: db.GetConfig("dhcp_lease_time"),
		EnableDns: db.GetConfig("dhcp_enable_dns") == "true",
	}
}

// SetDhcpConfig persists DHCP config to the KV store.
func (db *DB) SetDhcpConfig(c *DhcpConfig) {
	db.SetConfig("dhcp_interface", c.Interface)
	db.SetConfig("dhcp_start", c.DhcpStart)
	db.SetConfig("dhcp_end", c.DhcpEnd)
	db.SetConfig("dhcp_netmask", c.Netmask)
	db.SetConfig("dhcp_gateway", c.Gateway)
	db.SetConfig("dhcp_dns", c.Dns)
	db.SetConfig("dhcp_lease_time", c.LeaseTime)
	enableDns := "false"
	if c.EnableDns {
		enableDns = "true"
	}
	db.SetConfig("dhcp_enable_dns", enableDns)
}

// GetInterface returns the configured PXE network interface.
func (db *DB) GetInterface() string {
	return db.GetConfig("dhcp_interface")
}

// --- DHCP bindings (stored in DB, rendered to hosts file) ---

// DHCPBinding represents a static MAC→IP binding.
type DHCPBinding struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

// CreateDHCPBinding adds a static binding.
func (db *DB) CreateDHCPBinding(mac, ip, hostname string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`INSERT OR REPLACE INTO dhcp_bindings (mac, ip, hostname) VALUES (?, ?, ?)`,
			NormMAC(mac), ip, hostname)
		return err
	})
}

// DeleteDHCPBinding removes a binding by MAC.
func (db *DB) DeleteDHCPBinding(mac string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`DELETE FROM dhcp_bindings WHERE mac = ?`, NormMAC(mac))
		return err
	})
}

// ListDHCPBindings returns all static bindings.
func (db *DB) ListDHCPBindings() ([]DHCPBinding, error) {
	rows, err := db.conn.Query(`SELECT mac, ip, hostname FROM dhcp_bindings ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bindings []DHCPBinding
	for rows.Next() {
		var b DHCPBinding
		if err := rows.Scan(&b.MAC, &b.IP, &b.Hostname); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}
