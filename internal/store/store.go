// Package store provides a backward-compatible wrapper around the SQLite DB.
// Existing handler code calls Store methods; this bridges to the db package.
package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/db"
)

// Store wraps the SQLite DB for backward compatibility with existing handlers.
type Store struct {
	cfg *config.Config
	DB  *db.DB
}

func New(cfg *config.Config) *Store {
	dbPath := filepath.Join(cfg.DataDir(), "pxe.db")
	os.MkdirAll(filepath.Dir(dbPath), 0o755)

	database, err := db.Open(dbPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		fmt.Fprintf(os.Stderr, "FATAL: failed to open database: %v\n", err)
		os.Exit(1)
	}

	return &Store{cfg: cfg, DB: database}
}

// EnsureDirs creates required data directories.
func EnsureDirs(cfg *config.Config) {
	os.MkdirAll(cfg.DataDir(), 0o755)
	os.MkdirAll(cfg.DnsmasqConfDir(), 0o755)
	bootDir := cfg.BootDir()
	os.MkdirAll(filepath.Join(bootDir, "iso"), 0o755)
	os.MkdirAll(filepath.Join(bootDir, "http"), 0o755)
}

// Close closes the database.
func (s *Store) Close() error {
	return s.DB.Close()
}

// --- Types (backward compat with handler code) ---

// Task matches the handler's expected structure.
// Bid is set to SN for backward compat (handler uses it for archive naming).
type Task struct {
	ID             int             `json:"id"`
	Bid            string          `json:"bid"`
	SN             string          `json:"sn"`
	Hostname       string          `json:"hostname"`
	IP             string          `json:"ip"`
	OSID           string          `json:"os"`
	RootPassword   string          `json:"root_password"`
	DiskTargetSize int             `json:"disk_target_size"`
	Network        json.RawMessage `json:"network,omitempty"`
	Partition      json.RawMessage `json:"partition,omitempty"`
	Scripts        json.RawMessage `json:"scripts,omitempty"`
	Files          json.RawMessage `json:"files,omitempty"`
	SSHKeys        json.RawMessage `json:"ssh_keys,omitempty"`
	Status         string          `json:"status"`
}

// GetBondSlaveMACs returns bond slave MAC addresses.
func (t *Task) GetBondSlaveMACs() []string {
	if len(t.Network) == 0 {
		return nil
	}
	var net struct {
		Bond *struct {
			Slaves []string `json:"slaves"`
		} `json:"bond"`
	}
	if json.Unmarshal(t.Network, &net) == nil && net.Bond != nil {
		return net.Bond.Slaves
	}
	return nil
}

// dbTaskToStore converts db.Task to store.Task (adds Bid field).
func dbTaskToStore(t *db.Task) *Task {
	if t == nil {
		return nil
	}
	return &Task{
		ID:             t.ID,
		Bid:            t.SN, // Use SN as Bid for backward compat
		SN:             t.SN,
		Hostname:       t.Hostname,
		IP:             t.IP,
		OSID:           t.OS,
		RootPassword:   t.RootPassword,
		DiskTargetSize: t.DiskTargetSize,
		Network:        json.RawMessage(t.Network),
		Partition:      json.RawMessage(t.Partition),
		Scripts:        json.RawMessage(t.Scripts),
		Files:          json.RawMessage(t.Files),
		SSHKeys:        json.RawMessage(t.SSHKeys),
		Status:         t.Status,
	}
}

// OSTemplate matches the handler's expected structure (TplBid field name).
type OSTemplate struct {
	TplBid       string `json:"tpl_bid"`
	Label        string `json:"label"`
	DistroPath   string `json:"distro_path"`
	DistroFamily string `json:"distro_family"`
	BootType     string `json:"boot_type"`
	KernelArgs   string `json:"kernel_args"`
	Template     string `json:"template"`
	ISOPath      string `json:"iso_path"`
	MirrorURL    string `json:"mirror_url"`
}

func dbOSTemplateToStore(t *db.OSTemplate) OSTemplate {
	return OSTemplate{
		TplBid:       t.Bid,
		Label:        t.Label,
		DistroPath:   t.DistroPath,
		DistroFamily: t.DistroFamily,
		BootType:     t.BootType,
		KernelArgs:   t.KernelArgs,
		Template:     t.Template,
		ISOPath:      t.ISOPath,
		MirrorURL:    t.MirrorURL,
	}
}

// Result for backward compat with existing handler code.
type Result struct {
	TaskID       *int   `json:"task_id"`
	SN           string `json:"sn"`
	Status       string `json:"status"`
	Components   any    `json:"components,omitempty"`
	HardwareInfo any    `json:"hardware_info,omitempty"`
	InstallLog   string `json:"install_log,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// --- Task API ---

// GetAllTasks returns all tasks.
func (s *Store) GetAllTasks() []Task {
	dbTasks := s.DB.AllTasks()
	tasks := make([]Task, 0, len(dbTasks))
	for i := range dbTasks {
		tasks = append(tasks, *dbTaskToStore(&dbTasks[i]))
	}
	return tasks
}

// GetTaskBySN finds a task by SN. Returns (task, raw_json).
func (s *Store) GetTaskBySN(sn string) (*Task, []byte) {
	t, err := s.DB.GetTaskBySN(sn)
	if err != nil {
		return nil, nil
	}
	st := dbTaskToStore(t)
	data, _ := json.Marshal(st)
	return st, data
}

// GetTaskByMAC finds a task by MAC.
func (s *Store) GetTaskByMAC(mac string) *Task {
	t, err := s.DB.GetTaskByMAC(mac)
	if err != nil {
		return nil
	}
	return dbTaskToStore(t)
}

// RemoveTaskBySN deletes a task.
func (s *Store) RemoveTaskBySN(sn string) {
	s.DB.DeleteTask(sn)
}

// GetPxeServer returns cached PXE server address.
func (s *Store) GetPxeServer() (ip string, port string) {
	return s.DB.GetPxeServer()
}

// --- OS Templates ---

// GetOSTemplates returns all OS templates (backward compat).
func (s *Store) GetOSTemplates() []OSTemplate {
	dbTpls, _ := s.DB.ListOSTemplates()
	result := make([]OSTemplate, 0, len(dbTpls))
	for i := range dbTpls {
		result = append(result, dbOSTemplateToStore(&dbTpls[i]))
	}
	return result
}

// --- Templates (kickstart/cloud-init) — read from disk ---

// GetTemplate reads a template file by name from the templates directory.
func (s *Store) GetTemplate(name string) (string, bool) {
	p := filepath.Join(s.cfg.TemplatesDir(), name)
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// --- Results ---

// SaveResult persists a result.
func (s *Store) SaveResult(result Result) {
	r := &db.Result{
		SN:         result.SN,
		TaskID:     result.TaskID,
		Status:     result.Status,
		InstallLog: result.InstallLog,
	}
	if result.CompletedAt != "" {
		r.CompletedAt = result.CompletedAt
	}
	if result.Components != nil {
		b, _ := json.Marshal(result.Components)
		r.Components = string(b)
	}
	if result.HardwareInfo != nil {
		b, _ := json.Marshal(result.HardwareInfo)
		r.HardwareInfo = string(b)
	}
	s.DB.SaveResult(r)
}

// GetPendingResults returns recent results (no push needed, they stay in DB).
func (s *Store) GetPendingResults() ([]Result, []string) {
	dbResults, _ := s.DB.ListResults("", 50)
	var results []Result
	for _, r := range dbResults {
		results = append(results, Result{
			TaskID:      r.TaskID,
			SN:          r.SN,
			Status:      r.Status,
			InstallLog:  r.InstallLog,
			CompletedAt: r.CompletedAt,
		})
	}
	return results, nil
}

// RemoveResults is a no-op (results persist in DB).
func (s *Store) RemoveResults(paths []string) {}

// PendingResultsCount returns total result count.
func (s *Store) PendingResultsCount() int {
	results, _ := s.DB.ListResults("", 0)
	return len(results)
}

// --- Sync compat (used by syncHandler for bulk task push) ---

// SaveTasks bulk replaces tasks from a sync payload.
func (s *Store) SaveTasks(tasks []json.RawMessage, pxeIP, pxePort string) int {
	var creates []db.TaskCreate
	for _, raw := range tasks {
		var t struct {
			SN             string          `json:"sn"`
			Hostname       string          `json:"hostname"`
			IP             string          `json:"ip"`
			OS             string          `json:"os"`
			RootPassword   string          `json:"root_password"`
			DiskTargetSize int             `json:"disk_target_size"`
			Network        json.RawMessage `json:"network"`
			Partition      json.RawMessage `json:"partition"`
			Scripts        json.RawMessage `json:"scripts"`
			Files          json.RawMessage `json:"files"`
			SSHKeys        json.RawMessage `json:"ssh_keys"`
		}
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		creates = append(creates, db.TaskCreate{
			SN:             t.SN,
			Hostname:       t.Hostname,
			IP:             t.IP,
			OS:             t.OS,
			RootPassword:   t.RootPassword,
			DiskTargetSize: t.DiskTargetSize,
			Network:        string(t.Network),
			Partition:      string(t.Partition),
			Scripts:        string(t.Scripts),
			Files:          string(t.Files),
			SSHKeys:        string(t.SSHKeys),
		})
	}
	count, _ := s.DB.BatchCreateTasks(creates)
	if pxeIP != "" {
		s.DB.SetPxeServer(pxeIP, pxePort)
	}
	return count
}

// SaveOSTemplates saves OS templates from sync payload.
func (s *Store) SaveOSTemplates(raw []json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var tpls []db.OSTemplate
	for _, r := range raw {
		var t db.OSTemplate
		if json.Unmarshal(r, &t) == nil && t.Bid != "" {
			tpls = append(tpls, t)
			continue
		}
		// Old format with tpl_bid
		var old struct {
			TplBid       string `json:"tpl_bid"`
			Label        string `json:"label"`
			DistroPath   string `json:"distro_path"`
			DistroFamily string `json:"distro_family"`
			BootType     string `json:"boot_type"`
			KernelArgs   string `json:"kernel_args"`
			Template     string `json:"template"`
			ISOPath      string `json:"iso_path"`
			MirrorURL    string `json:"mirror_url"`
		}
		if json.Unmarshal(r, &old) == nil && old.TplBid != "" {
			tpls = append(tpls, db.OSTemplate{
				Bid:          old.TplBid,
				Label:        old.Label,
				DistroPath:   old.DistroPath,
				DistroFamily: old.DistroFamily,
				BootType:     old.BootType,
				KernelArgs:   old.KernelArgs,
				ISOPath:      old.ISOPath,
				MirrorURL:    old.MirrorURL,
				Template:     old.Template,
			})
		}
	}
	s.DB.BulkUpsertOSTemplates(tpls)
}

// SaveTemplates writes template files to disk from a sync payload.
func (s *Store) SaveTemplates(tpls map[string]string) {
	tplDir := s.cfg.TemplatesDir()
	for name, content := range tpls {
		p := filepath.Join(tplDir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
}

// GetSyncVersion returns the last applied sync version.
func (s *Store) GetSyncVersion() int64 {
	return s.DB.GetSyncVersion()
}

// SetSyncVersion stores the sync version.
func (s *Store) SetSyncVersion(version int64) {
	s.DB.SetSyncVersion(version)
}

// --- Helpers ---

// NormMAC normalizes MAC address.
func NormMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
}
