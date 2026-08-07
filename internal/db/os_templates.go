package db

import "database/sql"

// OSTemplate represents an OS template for PXE boot.
type OSTemplate struct {
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

// UpsertOSTemplate creates or replaces an OS template.
func (db *DB) UpsertOSTemplate(t *OSTemplate) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`
			INSERT OR REPLACE INTO os_templates (bid, label, distro_path, distro_family, boot_type, kernel_args, iso_path, mirror_url, template, script_bids, file_bids)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.Bid, t.Label, t.DistroPath, t.DistroFamily, t.BootType, t.KernelArgs, t.ISOPath, t.MirrorURL, t.Template, t.ScriptBids, t.FileBids,
		)
		return err
	})
}

// GetOSTemplate retrieves an OS template by bid.
func (db *DB) GetOSTemplate(bid string) (*OSTemplate, error) {
	var t OSTemplate
	err := db.conn.QueryRow(`SELECT bid, label, distro_path, distro_family, boot_type, kernel_args, iso_path, mirror_url, template, script_bids, file_bids FROM os_templates WHERE bid = ?`, bid).Scan(
		&t.Bid, &t.Label, &t.DistroPath, &t.DistroFamily, &t.BootType, &t.KernelArgs, &t.ISOPath, &t.MirrorURL, &t.Template, &t.ScriptBids, &t.FileBids,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListOSTemplates returns all OS templates.
func (db *DB) ListOSTemplates() ([]OSTemplate, error) {
	rows, err := db.conn.Query(`SELECT bid, label, distro_path, distro_family, boot_type, kernel_args, iso_path, mirror_url, template, script_bids, file_bids FROM os_templates ORDER BY bid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []OSTemplate
	for rows.Next() {
		var t OSTemplate
		if err := rows.Scan(&t.Bid, &t.Label, &t.DistroPath, &t.DistroFamily, &t.BootType, &t.KernelArgs, &t.ISOPath, &t.MirrorURL, &t.Template, &t.ScriptBids, &t.FileBids); err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

// DeleteOSTemplate removes an OS template by bid.
func (db *DB) DeleteOSTemplate(bid string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`DELETE FROM os_templates WHERE bid = ?`, bid)
		return err
	})
}

// GetOSTemplateMap returns templates indexed by bid (for rendering).
func (db *DB) GetOSTemplateMap() map[string]*OSTemplate {
	templates, err := db.ListOSTemplates()
	if err != nil {
		return nil
	}
	m := make(map[string]*OSTemplate, len(templates))
	for i := range templates {
		m[templates[i].Bid] = &templates[i]
	}
	return m
}

// CountOSTemplates returns the number of OS templates.
func (db *DB) CountOSTemplates() int {
	var count int
	db.conn.QueryRow(`SELECT COUNT(*) FROM os_templates`).Scan(&count)
	return count
}

// --- Bulk import (compat with sync) ---

// BulkUpsertOSTemplates replaces all OS templates (for sync compat).
func (db *DB) BulkUpsertOSTemplates(templates []OSTemplate) error {
	return db.WithWrite(func() error {
		tx, err := db.conn.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// Don't delete existing — upsert preserves user additions
		stmt, err := tx.Prepare(`
			INSERT OR REPLACE INTO os_templates (bid, label, distro_path, distro_family, boot_type, kernel_args, iso_path, mirror_url, template, script_bids, file_bids)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, t := range templates {
			if t.Bid == "" {
				continue
			}
			_, err := stmt.Exec(t.Bid, t.Label, t.DistroPath, t.DistroFamily, t.BootType, t.KernelArgs, t.ISOPath, t.MirrorURL, t.Template, t.ScriptBids, t.FileBids)
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// --- Backward compat type alias ---

// OSTemplateCompat matches the old store.OSTemplate JSON shape (tpl_bid field name).
type OSTemplateCompat struct {
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

// FindOSTemplateByBid returns the OS template for rendering (nil if not found).
func (db *DB) FindOSTemplateByBid(bid string) *OSTemplate {
	t, err := db.GetOSTemplate(bid)
	if err == sql.ErrNoRows || err != nil {
		return nil
	}
	return t
}
