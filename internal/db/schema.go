package db

import "log/slog"

const schemaVersion = 1

// migrate runs schema DDL. Idempotent via IF NOT EXISTS.
func (db *DB) migrate() error {
	stmts := []string{
		// Tasks (core table)
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sn TEXT UNIQUE NOT NULL,
			hostname TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT '',
			root_password TEXT NOT NULL DEFAULT 'CentOS@2026',
			disk_target_size INTEGER DEFAULT 480,
			network TEXT DEFAULT '{}',
			partition TEXT DEFAULT '{}',
			scripts TEXT DEFAULT '[]',
			files TEXT DEFAULT '[]',
			ssh_keys TEXT DEFAULT '[]',
			status TEXT DEFAULT 'pending',
			created_at TEXT DEFAULT (datetime('now')),
			started_at TEXT,
			completed_at TEXT,
			install_log TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)`,

		// OS templates
		`CREATE TABLE IF NOT EXISTS os_templates (
			bid TEXT PRIMARY KEY,
			label TEXT NOT NULL DEFAULT '',
			distro_path TEXT NOT NULL DEFAULT '',
			distro_family TEXT NOT NULL DEFAULT '',
			boot_type TEXT NOT NULL DEFAULT 'kickstart',
			kernel_args TEXT DEFAULT '',
			iso_path TEXT DEFAULT '',
			mirror_url TEXT DEFAULT '',
			template TEXT DEFAULT '',
			script_bids TEXT DEFAULT '[]',
			file_bids TEXT DEFAULT '[]'
		)`,

		// Kickstart / cloud-init templates
		`CREATE TABLE IF NOT EXISTS templates (
			name TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			updated_at TEXT DEFAULT (datetime('now'))
		)`,

		// Install results (history)
		`CREATE TABLE IF NOT EXISTS results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sn TEXT NOT NULL,
			task_id INTEGER,
			status TEXT NOT NULL,
			components TEXT DEFAULT '',
			hardware_info TEXT DEFAULT '',
			install_log TEXT DEFAULT '',
			completed_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_results_sn ON results(sn)`,

		// Scripts (custom post-install scripts, content stored in DB)
		`CREATE TABLE IF NOT EXISTS scripts (
			bid TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			script_type TEXT DEFAULT 'bash',
			description TEXT DEFAULT '',
			content TEXT NOT NULL DEFAULT ''
		)`,

		// DHCP static bindings
		`CREATE TABLE IF NOT EXISTS dhcp_bindings (
			mac TEXT PRIMARY KEY,
			ip TEXT NOT NULL,
			hostname TEXT DEFAULT ''
		)`,

		// File metadata
		`CREATE TABLE IF NOT EXISTS files (
			bid TEXT PRIMARY KEY,
			filename TEXT NOT NULL,
			path TEXT NOT NULL,
			dest_dir TEXT DEFAULT '/tmp/drivers',
			size INTEGER DEFAULT 0,
			sha256 TEXT DEFAULT '',
			created_at TEXT DEFAULT (datetime('now'))
		)`,

		// ISO images state
		`CREATE TABLE IF NOT EXISTS iso_images (
			filename TEXT PRIMARY KEY,
			distro_path TEXT DEFAULT '',
			mounted INTEGER DEFAULT 0,
			size INTEGER DEFAULT 0,
			downloaded_at TEXT
		)`,

		// KV config store
		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,

		// Schema version
		`INSERT OR IGNORE INTO config (key, value) VALUES ('schema_version', '1')`,
	}

	for _, stmt := range stmts {
		if _, err := db.conn.Exec(stmt); err != nil {
			slog.Error("migration error", "stmt", stmt[:min(80, len(stmt))])
			return err
		}
	}

	// Schema migrations (additive, safe to re-run)
	alterStmts := []string{
		`ALTER TABLE os_templates ADD COLUMN script_bids TEXT DEFAULT '[]'`,
		`ALTER TABLE os_templates ADD COLUMN file_bids TEXT DEFAULT '[]'`,
	}
	for _, stmt := range alterStmts {
		db.conn.Exec(stmt) // ignore "duplicate column" errors
	}

	// Destructive migration: task mac column removed (mac lives in network JSON).
	// Index must go before the column; errors ignored for fresh DBs.
	dropStmts := []string{
		`DROP INDEX IF EXISTS idx_tasks_mac`,
		`ALTER TABLE tasks DROP COLUMN mac`,
	}
	for _, stmt := range dropStmts {
		db.conn.Exec(stmt)
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
