package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/joyops/infra-pxe/internal/server"

	"github.com/spf13/cobra"
)

const defaultAddr = "http://127.0.0.1:9200"

type client struct {
	addr   string
	http   *http.Client
	stdout io.Writer
}

type serveFunc func(configPath string) error

type options struct {
	addr       string
	configPath string
	serve      serveFunc
}

// Execute runs the infra-pxe CLI with injectable streams for tests.
func Execute(args []string, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	opts := &options{addr: defaultAddr, configPath: "conf/pxe.yaml", serve: server.Run}
	cmd := newRootCommand(opts)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}
	return nil
}

func newRootCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "infra-pxe",
		Short:         "Operate an infra-pxe engine",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.serve(opts.configPath)
		},
	}
	cmd.Flags().StringVar(&opts.configPath, "config", "conf/pxe.yaml", "path to PXE config file")
	cmd.PersistentFlags().StringVar(&opts.addr, "addr", defaultAddr, "PXE Engine base URL")
	cmd.AddCommand(
		newServeCommand(opts),
		newHealthCommand(opts),
		newStatusCommand(opts),
		newSeedCommand(opts),
		newInterfacesCommand(opts),
		newDnsmasqCommand(opts),
		newDHCPCommand(opts),
		newTaskCommand(opts),
		newOSTemplateCommand(opts),
		newISOCommand(opts),
		newResultCommand(opts),
	)
	return cmd
}

func newServeCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the PXE Engine server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.serve(opts.configPath)
		},
	}
	cmd.Flags().StringVar(&opts.configPath, "config", "conf/pxe.yaml", "path to PXE config file")
	return cmd
}

func newClient(cmd *cobra.Command, opts *options) (*client, error) {
	addr := strings.TrimRight(opts.addr, "/")
	if _, err := url.ParseRequestURI(addr); err != nil {
		return nil, fmt.Errorf("invalid --addr: %w", err)
	}
	return &client{
		addr:   addr,
		http:   &http.Client{Timeout: 30 * time.Second},
		stdout: cmd.OutOrStdout(),
	}, nil
}

func (c *client) get(path string) error {
	return c.do(http.MethodGet, path, nil)
}

func (c *client) post(path string, payload any) error {
	return c.do(http.MethodPost, path, payload)
}

func (c *client) put(path string, payload any) error {
	return c.do(http.MethodPut, path, payload)
}

func (c *client) delete(path string) error {
	return c.do(http.MethodDelete, path, nil)
}

func (c *client) do(method, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.addr+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(data)))
	}
	return printJSON(c.stdout, data)
}

func printJSON(w io.Writer, data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		_, err := fmt.Fprintln(w, "{}")
		return err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(pretty))
	return err
}

func withClient(opts *options, run func(*client) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		c, err := newClient(cmd, opts)
		if err != nil {
			return err
		}
		return run(c)
	}
}

func newHealthCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check engine health",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/health") }),
	}
}

func newStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show engine status",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/system/status") }),
	}
}

func newSeedCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "seed", Short: "Manage seed data"}
	cmd.AddCommand(&cobra.Command{
		Use:   "import",
		Short: "Import bundled seed data",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.post("/api/seed/import", map[string]any{}) }),
	})
	return cmd
}

func newInterfacesCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "interfaces",
		Short: "List local network interfaces",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/interfaces") }),
	}
}

func newDnsmasqCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "dnsmasq", Short: "Control dnsmasq"}
	for _, action := range []string{"start", "stop", "reload"} {
		action := action
		cmd.AddCommand(&cobra.Command{
			Use:   action,
			Short: action + " dnsmasq",
			Args:  cobra.NoArgs,
			RunE:  withClient(opts, func(c *client) error { return c.post("/api/dnsmasq/"+action, map[string]any{}) }),
		})
	}
	return cmd
}

func newDHCPCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "dhcp", Short: "Manage DHCP"}
	config := &cobra.Command{Use: "config", Short: "Manage DHCP config"}
	config.AddCommand(newDHCPConfigGetCommand(opts), newDHCPConfigSetCommand(opts))
	bindings := &cobra.Command{Use: "binding", Aliases: []string{"bindings"}, Short: "Manage static DHCP bindings"}
	bindings.AddCommand(newDHCPBindingListCommand(opts), newDHCPBindingAddCommand(opts), newDHCPBindingDeleteCommand(opts))
	cmd.AddCommand(config, bindings, &cobra.Command{
		Use:   "leases",
		Short: "List DHCP leases",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/dhcp/leases") }),
	})
	return cmd
}

func newDHCPConfigGetCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Get DHCP config",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/dhcp/config") }),
	}
}

func newDHCPConfigSetCommand(opts *options) *cobra.Command {
	var req struct {
		Interface string `json:"interface"`
		DhcpStart string `json:"dhcp_start"`
		DhcpEnd   string `json:"dhcp_end"`
		Netmask   string `json:"netmask"`
		Gateway   string `json:"gateway"`
		Dns       string `json:"dns"`
		LeaseTime string `json:"lease_time"`
		EnableDNS bool   `json:"enable_dns"`
	}
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set DHCP config and restart dnsmasq",
		Args:  cobra.NoArgs,
		RunE: withClient(opts, func(c *client) error {
			return c.put("/api/dhcp/config", req)
		}),
	}
	cmd.Flags().StringVar(&req.Interface, "interface", "", "PXE interface")
	cmd.Flags().StringVar(&req.DhcpStart, "start", "", "DHCP range start")
	cmd.Flags().StringVar(&req.DhcpEnd, "end", "", "DHCP range end")
	cmd.Flags().StringVar(&req.Netmask, "netmask", "", "DHCP netmask")
	cmd.Flags().StringVar(&req.Gateway, "gateway", "", "DHCP gateway")
	cmd.Flags().StringVar(&req.Dns, "dns", "", "DNS server")
	cmd.Flags().StringVar(&req.LeaseTime, "lease-time", "5m", "DHCP lease time")
	cmd.Flags().BoolVar(&req.EnableDNS, "enable-dns", false, "Enable dnsmasq DNS service")
	must(cmd.MarkFlagRequired("interface"))
	must(cmd.MarkFlagRequired("start"))
	must(cmd.MarkFlagRequired("end"))
	return cmd
}

func newDHCPBindingListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List static DHCP bindings",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.get("/api/dhcp/bindings") }),
	}
}

func newDHCPBindingAddCommand(opts *options) *cobra.Command {
	var req struct {
		MAC      string `json:"mac"`
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add static DHCP binding",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.post("/api/dhcp/bindings", req) }),
	}
	cmd.Flags().StringVar(&req.MAC, "mac", "", "MAC address")
	cmd.Flags().StringVar(&req.IP, "ip", "", "IP address")
	cmd.Flags().StringVar(&req.Hostname, "hostname", "", "Hostname")
	must(cmd.MarkFlagRequired("mac"))
	must(cmd.MarkFlagRequired("ip"))
	return cmd
}

func newDHCPBindingDeleteCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <mac>",
		Short: "Delete static DHCP binding",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd, opts)
			if err != nil {
				return err
			}
			return c.delete("/api/dhcp/bindings/" + url.PathEscape(args[0]))
		},
	}
}

func newTaskCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Aliases: []string{"tasks"}, Short: "Manage install tasks"}
	list := &cobra.Command{Use: "list", Short: "List tasks", Args: cobra.NoArgs, RunE: taskListRun(opts)}
	list.Flags().String("status", "", "Filter by task status")
	cmd.AddCommand(
		list,
		&cobra.Command{Use: "get <sn>", Short: "Get task", Args: cobra.ExactArgs(1), RunE: pathGetRun(opts, "/api/tasks/")},
		newTaskCreateCommand(opts),
		&cobra.Command{Use: "delete <sn>", Short: "Delete task", Args: cobra.ExactArgs(1), RunE: pathDeleteRun(opts, "/api/tasks/")},
	)
	return cmd
}

func taskListRun(opts *options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		c, err := newClient(cmd, opts)
		if err != nil {
			return err
		}
		status, _ := cmd.Flags().GetString("status")
		path := "/api/tasks"
		if status != "" {
			path += "?status=" + url.QueryEscape(status)
		}
		return c.get(path)
	}
}

func newTaskCreateCommand(opts *options) *cobra.Command {
	var flags struct {
		SN             string
		Hostname       string
		IP             string
		OS             string
		MAC            string
		Network        string
		RootPassword   string
		DiskTargetSize int
		Partition      string
		Scripts        string
		Files          string
		SSHKeys        string
	}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create install task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			network := flags.Network
			if network == "" {
				if flags.MAC == "" || flags.IP == "" {
					return errors.New("either --network or both --mac and --ip are required")
				}
				b, err := json.Marshal(map[string]string{"type": "static", "mac": flags.MAC, "ip": flags.IP})
				if err != nil {
					return err
				}
				network = string(b)
			}
			req := map[string]any{
				"sn":               flags.SN,
				"hostname":         flags.Hostname,
				"ip":               flags.IP,
				"os":               flags.OS,
				"root_password":    flags.RootPassword,
				"disk_target_size": flags.DiskTargetSize,
				"network":          network,
			}
			addOptional(req, "partition", flags.Partition)
			addOptional(req, "scripts", flags.Scripts)
			addOptional(req, "files", flags.Files)
			addOptional(req, "ssh_keys", flags.SSHKeys)
			c, err := newClient(cmd, opts)
			if err != nil {
				return err
			}
			return c.post("/api/tasks", req)
		},
	}
	cmd.Flags().StringVar(&flags.SN, "sn", "", "Server serial number")
	cmd.Flags().StringVar(&flags.Hostname, "hostname", "", "Target hostname")
	cmd.Flags().StringVar(&flags.IP, "ip", "", "Target IP")
	cmd.Flags().StringVar(&flags.OS, "os", "", "OS template bid")
	cmd.Flags().StringVar(&flags.MAC, "mac", "", "Target MAC")
	cmd.Flags().StringVar(&flags.Network, "network", "", "Raw task network JSON")
	cmd.Flags().StringVar(&flags.RootPassword, "root-password", "", "Root password")
	cmd.Flags().IntVar(&flags.DiskTargetSize, "disk-target-size", 0, "Disk target size in GB")
	cmd.Flags().StringVar(&flags.Partition, "partition", "", "Raw partition JSON")
	cmd.Flags().StringVar(&flags.Scripts, "scripts", "", "Raw scripts JSON")
	cmd.Flags().StringVar(&flags.Files, "files", "", "Raw files JSON")
	cmd.Flags().StringVar(&flags.SSHKeys, "ssh-keys", "", "Raw SSH keys JSON")
	must(cmd.MarkFlagRequired("sn"))
	must(cmd.MarkFlagRequired("os"))
	return cmd
}

func newOSTemplateCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "os-template", Aliases: []string{"os-templates", "template"}, Short: "Manage OS templates"}
	cmd.AddCommand(
		&cobra.Command{Use: "list", Short: "List OS templates", Args: cobra.NoArgs, RunE: withClient(opts, func(c *client) error { return c.get("/api/os-templates") })},
		&cobra.Command{Use: "get <bid>", Short: "Get OS template", Args: cobra.ExactArgs(1), RunE: pathGetRun(opts, "/api/os-templates/")},
		newOSTemplateUpdateCommand(opts),
		&cobra.Command{Use: "delete <bid>", Short: "Delete OS template", Args: cobra.ExactArgs(1), RunE: pathDeleteRun(opts, "/api/os-templates/")},
	)
	return cmd
}

func newOSTemplateUpdateCommand(opts *options) *cobra.Command {
	var req struct {
		Bid          string `json:"bid"`
		Label        string `json:"label"`
		DistroPath   string `json:"distro_path"`
		DistroFamily string `json:"distro_family"`
		BootType     string `json:"boot_type"`
		KernelArgs   string `json:"kernel_args"`
		ISOPath      string `json:"iso_path"`
		MirrorURL    string `json:"mirror_url"`
		Template     string `json:"template"`
		ScriptBids   string `json:"script_bids"`
		FileBids     string `json:"file_bids"`
	}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Create or update OS template",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.post("/api/os-templates", req) }),
	}
	cmd.Flags().StringVar(&req.Bid, "bid", "", "Template bid")
	cmd.Flags().StringVar(&req.Label, "label", "", "Label")
	cmd.Flags().StringVar(&req.DistroPath, "distro-path", "", "Distro path")
	cmd.Flags().StringVar(&req.DistroFamily, "distro-family", "", "Distro family")
	cmd.Flags().StringVar(&req.BootType, "boot-type", "", "Boot type")
	cmd.Flags().StringVar(&req.KernelArgs, "kernel-args", "", "Kernel args")
	cmd.Flags().StringVar(&req.ISOPath, "iso-path", "", "ISO path")
	cmd.Flags().StringVar(&req.MirrorURL, "mirror-url", "", "Mirror URL")
	cmd.Flags().StringVar(&req.Template, "template", "", "Template filename")
	cmd.Flags().StringVar(&req.ScriptBids, "script-bids", "", "Comma-separated script bids")
	cmd.Flags().StringVar(&req.FileBids, "file-bids", "", "Comma-separated file bids")
	must(cmd.MarkFlagRequired("bid"))
	return cmd
}

func newISOCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "iso", Short: "Manage ISO files"}
	cmd.AddCommand(
		&cobra.Command{Use: "list", Short: "List ISO files", Args: cobra.NoArgs, RunE: withClient(opts, func(c *client) error { return c.get("/api/iso/list") })},
		newISOMountCommand(opts),
		newISODownloadCommand(opts),
		&cobra.Command{Use: "mounted", Short: "List mounted ISOs", Args: cobra.NoArgs, RunE: withClient(opts, func(c *client) error { return c.get("/api/iso/mounted") })},
	)
	return cmd
}

func newISOMountCommand(opts *options) *cobra.Command {
	var req struct {
		Filename   string `json:"filename"`
		DistroPath string `json:"distro_path"`
	}
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Mount ISO for HTTP serving",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.post("/api/iso/mount", req) }),
	}
	cmd.Flags().StringVar(&req.Filename, "filename", "", "ISO filename")
	cmd.Flags().StringVar(&req.DistroPath, "distro-path", "", "Distro path")
	must(cmd.MarkFlagRequired("filename"))
	return cmd
}

func newISODownloadCommand(opts *options) *cobra.Command {
	var req struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download ISO in background",
		Args:  cobra.NoArgs,
		RunE:  withClient(opts, func(c *client) error { return c.post("/api/iso/download", req) }),
	}
	cmd.Flags().StringVar(&req.URL, "url", "", "ISO URL")
	cmd.Flags().StringVar(&req.Filename, "filename", "", "Destination filename")
	must(cmd.MarkFlagRequired("url"))
	must(cmd.MarkFlagRequired("filename"))
	return cmd
}

func newResultCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "result", Aliases: []string{"results"}, Short: "Show install history"}
	list := &cobra.Command{Use: "list", Short: "List results", Args: cobra.NoArgs, RunE: resultsListRun(opts)}
	list.Flags().String("sn", "", "Filter by server serial number")
	cmd.AddCommand(
		list,
		&cobra.Command{Use: "get <sn>", Short: "Get results by SN", Args: cobra.ExactArgs(1), RunE: pathGetRun(opts, "/api/results/")},
	)
	return cmd
}

func resultsListRun(opts *options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		c, err := newClient(cmd, opts)
		if err != nil {
			return err
		}
		sn, _ := cmd.Flags().GetString("sn")
		path := "/api/results"
		if sn != "" {
			path += "?sn=" + url.QueryEscape(sn)
		}
		return c.get(path)
	}
}

func pathGetRun(opts *options, prefix string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := newClient(cmd, opts)
		if err != nil {
			return err
		}
		return c.get(prefix + url.PathEscape(args[0]))
	}
}

func pathDeleteRun(opts *options, prefix string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := newClient(cmd, opts)
		if err != nil {
			return err
		}
		return c.delete(prefix + url.PathEscape(args[0]))
	}
}

func addOptional(req map[string]any, key, value string) {
	if value != "" {
		req[key] = value
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
