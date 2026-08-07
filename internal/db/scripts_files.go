package db

import "strings"
type Script struct {
	Bid         string `json:"bid"`
	Name        string `json:"name"`
	ScriptType  string `json:"script_type"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// UpsertScript creates or replaces a script.
func (db *DB) UpsertScript(s *Script) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`
			INSERT OR REPLACE INTO scripts (bid, name, script_type, description, content)
			VALUES (?, ?, ?, ?, ?)`,
			s.Bid, s.Name, s.ScriptType, s.Description, s.Content,
		)
		return err
	})
}

// GetScript retrieves a script by bid.
func (db *DB) GetScript(bid string) (*Script, error) {
	var s Script
	err := db.conn.QueryRow(`SELECT bid, name, script_type, description, content FROM scripts WHERE bid = ?`, bid).Scan(
		&s.Bid, &s.Name, &s.ScriptType, &s.Description, &s.Content,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListScripts returns all scripts.
func (db *DB) ListScripts() ([]Script, error) {
	rows, err := db.conn.Query(`SELECT bid, name, script_type, description, content FROM scripts ORDER BY bid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scripts []Script
	for rows.Next() {
		var s Script
		if err := rows.Scan(&s.Bid, &s.Name, &s.ScriptType, &s.Description, &s.Content); err != nil {
			return nil, err
		}
		scripts = append(scripts, s)
	}
	return scripts, rows.Err()
}

// DeleteScript removes a script by bid.
func (db *DB) DeleteScript(bid string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`DELETE FROM scripts WHERE bid = ?`, bid)
		return err
	})
}

// ResolveScripts takes a comma-separated string of script bids and returns full script objects.
func (db *DB) ResolveScripts(bidsStr string) []Script {
	bids := ParseBids(bidsStr)
	if len(bids) == 0 {
		return nil
	}
	var result []Script
	for _, bid := range bids {
		s, err := db.GetScript(bid)
		if err == nil && s != nil {
			result = append(result, *s)
		}
	}
	return result
}

// --- File CRUD additions ---

// File represents a post-install file (metadata; physical file on disk).
type File struct {
	Bid      string `json:"bid"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	DestDir  string `json:"dest_dir"`
	Size     int64  `json:"size"`
	Sha256   string `json:"sha256"`
}

// UpsertFile creates or replaces a file record.
func (db *DB) UpsertFile(f *File) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`
			INSERT OR REPLACE INTO files (bid, filename, path, dest_dir, size, sha256)
			VALUES (?, ?, ?, ?, ?, ?)`,
			f.Bid, f.Filename, f.Path, f.DestDir, f.Size, f.Sha256,
		)
		return err
	})
}

// GetFile retrieves a file by bid.
func (db *DB) GetFile(bid string) (*File, error) {
	var f File
	err := db.conn.QueryRow(`SELECT bid, filename, path, dest_dir, size, sha256 FROM files WHERE bid = ?`, bid).Scan(
		&f.Bid, &f.Filename, &f.Path, &f.DestDir, &f.Size, &f.Sha256,
	)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ListFiles returns all file records.
func (db *DB) ListFiles() ([]File, error) {
	rows, err := db.conn.Query(`SELECT bid, filename, path, dest_dir, size, sha256 FROM files ORDER BY bid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Bid, &f.Filename, &f.Path, &f.DestDir, &f.Size, &f.Sha256); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// DeleteFile removes a file record by bid.
func (db *DB) DeleteFile(bid string) error {
	return db.WithWrite(func() error {
		_, err := db.conn.Exec(`DELETE FROM files WHERE bid = ?`, bid)
		return err
	})
}

// --- helpers ---

// ParseBids splits a comma-separated bid string into a slice.
func ParseBids(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
