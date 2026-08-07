package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCommandRequestsConfiguredAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/health" {
			t.Fatalf("request = %s %s, want GET /api/health", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "health"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "ok") {
		t.Fatalf("stdout = %q, want health status", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTaskCreateBuildsNetworkPayload(t *testing.T) {
	t.Parallel()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tasks" {
			t.Fatalf("request = %s %s, want POST /api/tasks", r.Method, r.URL.Path)
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		body = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","message":"ok","data":{"sn":"SN001","status":"pending"}}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "task", "create", "--sn", "SN001", "--hostname", "node-1", "--ip", "10.0.0.10", "--os", "tpl-euler03x64-std", "--mac", "aa:bb:cc:dd:ee:ff"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
	got := decodeJSON(t, body)
	for key, want := range map[string]any{
		"sn":       "SN001",
		"hostname": "node-1",
		"ip":       "10.0.0.10",
		"os":       "tpl-euler03x64-std",
		"network":  `{"ip":"10.0.0.10","mac":"aa:bb:cc:dd:ee:ff","type":"static"}`,
	} {
		if got[key] != want {
			t.Fatalf("request body %s: %s = %v, want %v", body, key, got[key], want)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "SN001") || !strings.Contains(got, "pending") {
		t.Fatalf("stdout = %q, want created task", got)
	}
}

func TestDHCPConfigSetSendsUpdate(t *testing.T) {
	t.Parallel()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/dhcp/config" {
			t.Fatalf("request = %s %s, want PUT /api/dhcp/config", r.Method, r.URL.Path)
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		body = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","message":"ok","data":{"interface":"eth0","dhcp_start":"10.0.0.100","dhcp_end":"10.0.0.200","running":true}}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "dhcp", "config", "set", "--interface", "eth0", "--start", "10.0.0.100", "--end", "10.0.0.200", "--netmask", "255.255.255.0", "--gateway", "10.0.0.1", "--dns", "223.5.5.5", "--lease-time", "5m", "--enable-dns"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
	for _, want := range []string{`"interface":"eth0"`, `"dhcp_start":"10.0.0.100"`, `"dhcp_end":"10.0.0.200"`, `"netmask":"255.255.255.0"`, `"gateway":"10.0.0.1"`, `"dns":"223.5.5.5"`, `"lease_time":"5m"`, `"enable_dns":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("request body = %s, missing %s", body, want)
		}
	}
}

func TestDHCPBindingDeleteUsesMACArgument(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/dhcp/bindings/aa:bb:cc:dd:ee:ff" {
			t.Fatalf("request = %s %s, want DELETE /api/dhcp/bindings/aa:bb:cc:dd:ee:ff", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","message":"ok"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "dhcp", "binding", "delete", "aa:bb:cc:dd:ee:ff"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
}

func TestTaskListSupportsStatusFilter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks" || r.URL.Query().Get("status") != "pending" {
			t.Fatalf("request = %s %s?%s, want GET /api/tasks?status=pending", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "task", "list", "--status", "pending"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
}

func TestResultListSupportsSNFilter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/results" || r.URL.Query().Get("sn") != "SN001" {
			t.Fatalf("request = %s %s?%s, want GET /api/results?sn=SN001", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--addr", server.URL, "result", "list", "--sn", "SN001"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
}

func TestServeCommandUsesConfigFlag(t *testing.T) {
	t.Parallel()
	var gotConfig string
	opts := &options{
		addr:       defaultAddr,
		configPath: "conf/pxe.yaml",
		serve: func(configPath string) error {
			gotConfig = configPath
			return nil
		},
	}
	cmd := newRootCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"serve", "--config", "custom.yaml"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
	if gotConfig != "custom.yaml" {
		t.Fatalf("serve config path = %q, want custom.yaml", gotConfig)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want both empty", stdout.String(), stderr.String())
	}
}

func TestRootWithoutSubcommandDefaultsToServe(t *testing.T) {
	t.Parallel()
	var gotConfig string
	opts := &options{
		addr:       defaultAddr,
		configPath: "conf/pxe.yaml",
		serve: func(configPath string) error {
			gotConfig = configPath
			return nil
		},
	}
	cmd := newRootCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--config", "legacy.yaml"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v; stderr=%q", err, stderr.String())
	}
	if gotConfig != "legacy.yaml" {
		t.Fatalf("default serve config path = %q, want legacy.yaml", gotConfig)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want both empty", stdout.String(), stderr.String())
	}
}
func decodeJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal request body %q: %v", body, err)
	}
	return got
}
