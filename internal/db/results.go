package db

import (
	"encoding/json"
	"time"
)

// Result represents an install result (history).
type Result struct {
	ID           int    `json:"id"`
	SN           string `json:"sn"`
	TaskID       *int   `json:"task_id,omitempty"`
	Status       string `json:"status"`
	Components   string `json:"components,omitempty"`
	HardwareInfo string `json:"hardware_info,omitempty"`
	InstallLog   string `json:"install_log,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// SaveResult persists a result to DB.
func (db *DB) SaveResult(r *Result) error {
	if r.CompletedAt == "" {
		r.CompletedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`
			INSERT INTO results (sn, task_id, status, components, hardware_info, install_log, completed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.SN, r.TaskID, r.Status, r.Components, r.HardwareInfo, r.InstallLog, r.CompletedAt,
		)
		return err
	})
}

// ListResults returns results with optional SN filter.
func (db *DB) ListResults(sn string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 100
	}
	var query string
	var args []any
	if sn != "" {
		query = `SELECT id, sn, task_id, status, components, hardware_info, install_log, completed_at FROM results WHERE sn = ? ORDER BY id DESC LIMIT ?`
		args = []any{sn, limit}
	} else {
		query = `SELECT id, sn, task_id, status, components, hardware_info, install_log, completed_at FROM results ORDER BY id DESC LIMIT ?`
		args = []any{limit}
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.ID, &r.SN, &r.TaskID, &r.Status, &r.Components, &r.HardwareInfo, &r.InstallLog, &r.CompletedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetResultsBySN returns all results for a given SN.
func (db *DB) GetResultsBySN(sn string) ([]Result, error) {
	return db.ListResults(sn, 100)
}

// ResultFromJSON creates a Result from a legacy JSON payload.
func ResultFromJSON(data []byte) (*Result, error) {
	var r struct {
		TaskID       *int   `json:"task_id"`
		SN           string `json:"sn"`
		Status       string `json:"status"`
		Components   any    `json:"components"`
		HardwareInfo any    `json:"hardware_info"`
		InstallLog   string `json:"install_log"`
		CompletedAt  string `json:"completed_at"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	result := &Result{
		SN:          r.SN,
		TaskID:      r.TaskID,
		Status:      r.Status,
		InstallLog:  r.InstallLog,
		CompletedAt: r.CompletedAt,
	}
	if r.Components != nil {
		b, _ := json.Marshal(r.Components)
		result.Components = string(b)
	}
	if r.HardwareInfo != nil {
		b, _ := json.Marshal(r.HardwareInfo)
		result.HardwareInfo = string(b)
	}
	return result, nil
}
