package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Task represents a PXE install task.
type Task struct {
	ID             int    `json:"id"`
	SN             string `json:"sn"`
	Hostname       string `json:"hostname"`
	IP             string `json:"ip"`
	OS             string `json:"os"`
	RootPassword   string `json:"root_password"`
	DiskTargetSize int    `json:"disk_target_size"`
	Network        string `json:"network,omitempty"`
	Partition      string `json:"partition,omitempty"`
	Scripts        string `json:"scripts,omitempty"`
	Files          string `json:"files,omitempty"`
	SSHKeys        string `json:"ssh_keys,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	CompletedAt    string `json:"completed_at,omitempty"`
	InstallLog     string `json:"install_log,omitempty"`
}

// GetBondSlaveMACs returns MAC addresses of bond slave NICs.
func (t *Task) GetBondSlaveMACs() []string {
	if t.Network == "" || t.Network == "{}" {
		return nil
	}
	var net struct {
		Bond *struct {
			Slaves []string `json:"slaves"`
		} `json:"bond"`
	}
	if json.Unmarshal([]byte(t.Network), &net) == nil && net.Bond != nil {
		return net.Bond.Slaves
	}
	return nil
}

// GetNetworkMACs returns every MAC referenced in the network config:
// network.mac (single-port) plus bond.slaves (both bond ports).
func (t *Task) GetNetworkMACs() []string {
	if t.Network == "" || t.Network == "{}" {
		return nil
	}
	var net struct {
		MAC  string `json:"mac"`
		Bond *struct {
			Slaves []string `json:"slaves"`
		} `json:"bond"`
	}
	if json.Unmarshal([]byte(t.Network), &net) != nil {
		return nil
	}
	macs := make([]string, 0, 2)
	if net.MAC != "" {
		macs = append(macs, net.MAC)
	}
	if net.Bond != nil {
		macs = append(macs, net.Bond.Slaves...)
	}
	return macs
}

// TaskCreate is the input for creating a task.
type TaskCreate struct {
	SN             string `json:"sn"`
	Hostname       string `json:"hostname"`
	IP             string `json:"ip"`
	OS             string `json:"os"`
	RootPassword   string `json:"root_password"`
	DiskTargetSize int    `json:"disk_target_size"`
	Network        string `json:"network"`
	Partition      string `json:"partition"`
	Scripts        string `json:"scripts"`
	Files          string `json:"files"`
	SSHKeys        string `json:"ssh_keys"`
}

// CreateTask inserts a new task.
func (db *DB) CreateTask(t *TaskCreate) (*Task, error) {
	if t.SN == "" {
		return nil, fmt.Errorf("sn is required")
	}
	if t.RootPassword == "" {
		t.RootPassword = "CentOS@2026"
	}
	if t.DiskTargetSize == 0 {
		t.DiskTargetSize = 480
	}
	if t.Network == "" || t.Network == "{}" {
		return nil, fmt.Errorf("network is required")
	}
	if t.Partition == "" {
		t.Partition = "{}"
	}
	if t.Scripts == "" {
		t.Scripts = "[]"
	}
	if t.Files == "" {
		t.Files = "[]"
	}
	if t.SSHKeys == "" {
		t.SSHKeys = "[]"
	}

	var task Task
	err := db.WithWrite(func() error {
		res, err := db.conn.Exec(`
			INSERT INTO tasks (sn, hostname, ip, os, root_password, disk_target_size,
				network, partition, scripts, files, ssh_keys)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.SN, t.Hostname, t.IP, t.OS, t.RootPassword, t.DiskTargetSize,
			t.Network, t.Partition, t.Scripts, t.Files, t.SSHKeys,
		)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		task.ID = int(id)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Read back to get defaults
	return db.GetTaskBySN(t.SN)
}

// GetTaskBySN retrieves a task by serial number.
func (db *DB) GetTaskBySN(sn string) (*Task, error) {
	return db.scanTask(db.conn.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE sn = ?`, sn))
}

// GetTaskByMAC retrieves a task by MAC address.
// MACs live in the network JSON (network.mac single-port / bond.slaves bond),
// so this scans network JSON and matches any of them.
func (db *DB) GetTaskByMAC(mac string) (*Task, error) {
	mac = NormMAC(mac)
	rows, err := db.conn.Query(`SELECT id, sn, hostname, ip, os, root_password, disk_target_size,
		network, partition, scripts, files, ssh_keys, status, created_at, started_at, completed_at, install_log
		FROM tasks WHERE network LIKE ?`, "%"+mac+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err2 := db.scanTaskFromRows(rows)
		if err2 != nil {
			continue
		}
		for _, m := range t.GetNetworkMACs() {
			if NormMAC(m) == mac {
				return t, nil
			}
		}
	}
	return nil, sql.ErrNoRows
}

// GetTaskByID retrieves a task by ID.
func (db *DB) GetTaskByID(id int) (*Task, error) {
	return db.scanTask(db.conn.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
}

// ListTasks returns tasks with optional status filter.
func (db *DB) ListTasks(status string) ([]Task, error) {
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = db.conn.Query(`SELECT `+taskColumns+` FROM tasks WHERE status = ? ORDER BY id`, status)
	} else {
		rows, err = db.conn.Query(`SELECT `+taskColumns+` FROM tasks ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return db.scanTasks(rows)
}

// UpdateTask updates mutable fields of a task.
func (db *DB) UpdateTask(sn string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"hostname": true, "ip": true, "os": true,
		"root_password": true, "disk_target_size": true,
		"network": true, "partition": true, "scripts": true,
		"files": true, "ssh_keys": true, "status": true,
		"started_at": true, "completed_at": true, "install_log": true,
	}

	var sets []string
	var args []any
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, sn)

	return db.WithWrite(func() error {
		_, err := db.conn.Exec(
			fmt.Sprintf("UPDATE tasks SET %s WHERE sn = ?", strings.Join(sets, ", ")),
			args...,
		)
		return err
	})
}

// UpdateTaskStatus sets task status with timestamp.
func (db *DB) UpdateTaskStatus(sn, status string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	fields := map[string]any{"status": status}
	switch status {
	case "booting", "installing":
		fields["started_at"] = now
	case "installed", "failed":
		fields["completed_at"] = now
	}
	return db.UpdateTask(sn, fields)
}

// DeleteTask removes a task by SN.
func (db *DB) DeleteTask(sn string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`DELETE FROM tasks WHERE sn = ?`, sn)
		return err
	})
}

// BatchCreateTasks creates multiple tasks in one transaction.
func (db *DB) BatchCreateTasks(tasks []TaskCreate) (int, error) {
	count := 0
	err := db.WithWrite(func() error {
		tx, err := db.conn.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.Prepare(`
			INSERT OR REPLACE INTO tasks (sn, hostname, ip, os, root_password, disk_target_size,
				network, partition, scripts, files, ssh_keys)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, t := range tasks {
			if t.SN == "" {
				continue
			}
			if t.Network == "" || t.Network == "{}" {
				return fmt.Errorf("task %s: network is required", t.SN)
			}
			if t.RootPassword == "" {
				t.RootPassword = "CentOS@2026"
			}
			if t.DiskTargetSize == 0 {
				t.DiskTargetSize = 480
			}
			if t.Network == "" {
				t.Network = "{}"
			}
			if t.Partition == "" {
				t.Partition = "{}"
			}
			if t.Scripts == "" {
				t.Scripts = "[]"
			}
			if t.Files == "" {
				t.Files = "[]"
			}
			if t.SSHKeys == "" {
				t.SSHKeys = "[]"
			}
			_, err := stmt.Exec(t.SN, t.Hostname, t.IP, t.OS,
				t.RootPassword, t.DiskTargetSize,
				t.Network, t.Partition, t.Scripts, t.Files, t.SSHKeys)
			if err != nil {
				return err
			}
			count++
		}
		return tx.Commit()
	})
	return count, err
}

// AllTasks returns every task (for dnsmasq config generation).
func (db *DB) AllTasks() []Task {
	tasks, _ := db.ListTasks("")
	return tasks
}

// --- scan helpers ---

// taskColumns is the explicit SELECT column list — keeps scanning independent of
// physical table layout (e.g. if an old DB still carries the dropped mac column).
const taskColumns = `id, sn, hostname, ip, os, root_password, disk_target_size,
	network, partition, scripts, files, ssh_keys, status, created_at, started_at, completed_at, install_log`

func (db *DB) scanTask(row *sql.Row) (*Task, error) {
	var t Task
	var startedAt, completedAt, createdAt sql.NullString
	err := row.Scan(
		&t.ID, &t.SN, &t.Hostname, &t.IP, &t.OS,
		&t.RootPassword, &t.DiskTargetSize,
		&t.Network, &t.Partition, &t.Scripts, &t.Files, &t.SSHKeys,
		&t.Status, &createdAt, &startedAt, &completedAt, &t.InstallLog,
	)
	if err != nil {
		return nil, err
	}
	t.CreatedAt = createdAt.String
	t.StartedAt = startedAt.String
	t.CompletedAt = completedAt.String
	return &t, nil
}

func (db *DB) scanTaskFromRows(rows *sql.Rows) (*Task, error) {
	var t Task
	var startedAt, completedAt, createdAt sql.NullString
	err := rows.Scan(
		&t.ID, &t.SN, &t.Hostname, &t.IP, &t.OS,
		&t.RootPassword, &t.DiskTargetSize,
		&t.Network, &t.Partition, &t.Scripts, &t.Files, &t.SSHKeys,
		&t.Status, &createdAt, &startedAt, &completedAt, &t.InstallLog,
	)
	if err != nil {
		return nil, err
	}
	t.CreatedAt = createdAt.String
	t.StartedAt = startedAt.String
	t.CompletedAt = completedAt.String
	return &t, nil
}

func (db *DB) scanTasks(rows *sql.Rows) ([]Task, error) {
	var tasks []Task
	for rows.Next() {
		t, err := db.scanTaskFromRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, rows.Err()
}

// NormMAC normalizes a MAC address to lowercase colon-separated.
func NormMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
}
