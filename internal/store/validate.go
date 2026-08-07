package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/db"
)

// CheckResult represents a single validation check.
type CheckResult struct {
	Status   string `json:"status"`   // "ok" or "fail"
	Expected string `json:"expected,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// OSTemplateValidation is the full validation result for an OS template.
type OSTemplateValidation struct {
	Bid        string        `json:"bid"`
	Ready      bool          `json:"ready"`
	Template   CheckResult   `json:"template"`
	ISO        CheckResult   `json:"iso"`
	ISOMounted CheckResult   `json:"iso_mounted"`
	Scripts    []CheckResult `json:"scripts,omitempty"`
	Files      []CheckResult `json:"files,omitempty"`
}

// ValidateOSTemplate checks whether all physical resources for an OS template are present.
func (s *Store) ValidateOSTemplate(bid string) *OSTemplateValidation {
	tpl := s.DB.FindOSTemplateByBid(bid)
	if tpl == nil {
		return &OSTemplateValidation{
			Bid:   bid,
			Ready: false,
			Template: CheckResult{Status: "fail", Detail: "os_template not found"},
		}
	}

	v := &OSTemplateValidation{Bid: bid, Ready: true}

	// 1. Check kickstart/cloud-init template file
	if tpl.Template != "" {
		if _, ok := s.GetTemplate(tpl.Template); ok {
			v.Template = CheckResult{Status: "ok", Expected: tpl.Template}
		} else {
			v.Template = CheckResult{Status: "fail", Expected: tpl.Template, Detail: "template file not found"}
			v.Ready = false
		}
	} else {
		v.Template = CheckResult{Status: "ok", Detail: "no template required"}
	}

	// 2. Check ISO file exists on disk
	if tpl.ISOPath != "" {
		isoDir := filepath.Join(s.cfg.BootDir(), "iso")
		isoFullPath := filepath.Join(isoDir, tpl.ISOPath)
		if _, err := os.Stat(isoFullPath); err == nil {
			v.ISO = CheckResult{Status: "ok", Expected: tpl.ISOPath}
		} else {
			// Also check via symlink eval
			if resolved, err := filepath.EvalSymlinks(isoFullPath); err == nil {
				if _, err := os.Stat(resolved); err == nil {
					v.ISO = CheckResult{Status: "ok", Expected: tpl.ISOPath}
				} else {
					v.ISO = CheckResult{Status: "fail", Expected: tpl.ISOPath, Detail: "ISO file not found in boot/iso/"}
					v.Ready = false
				}
			} else {
				v.ISO = CheckResult{Status: "fail", Expected: tpl.ISOPath, Detail: "ISO file not found in boot/iso/"}
				v.Ready = false
			}
		}
	} else {
		v.ISO = CheckResult{Status: "ok", Detail: "no ISO required (network install)"}
	}

	// 3. Check ISO is mounted at the correct distro_path
	if tpl.DistroPath != "" {
		repoPath := filepath.Join(s.cfg.BootDir(), "http", tpl.DistroPath, "repo")
		if isMountPointOrHasContent(repoPath) {
			v.ISOMounted = CheckResult{Status: "ok", Expected: tpl.DistroPath}
		} else {
			v.ISOMounted = CheckResult{Status: "fail", Expected: tpl.DistroPath, Detail: "ISO not mounted at expected distro_path"}
			v.Ready = false
		}
	} else {
		v.ISOMounted = CheckResult{Status: "ok", Detail: "no distro_path"}
	}

	// 4. Check scripts referenced by os_template (stored in DB)
	if tpl.ScriptBids != "" {
		scriptBids := db.ParseBids(tpl.ScriptBids)
		for _, bid := range scriptBids {
			sc, err := s.DB.GetScript(bid)
			if err == nil && sc != nil && sc.Content != "" {
				v.Scripts = append(v.Scripts, CheckResult{Status: "ok", Expected: bid})
			} else {
				v.Scripts = append(v.Scripts, CheckResult{Status: "fail", Expected: bid, Detail: "script not found in DB"})
				v.Ready = false
			}
		}
	}

	// 5. Check files referenced by os_template
	if tpl.FileBids != "" {
		fileBids := db.ParseBids(tpl.FileBids)
		httpDir := filepath.Join(s.cfg.BootDir(), "http")
		for _, bid := range fileBids {
			// Files are stored in boot/http/{bid}/ directory
			fileDir := filepath.Join(httpDir, bid)
			if entries, err := os.ReadDir(fileDir); err == nil && len(entries) > 0 {
				v.Files = append(v.Files, CheckResult{Status: "ok", Expected: bid})
			} else {
				v.Files = append(v.Files, CheckResult{Status: "fail", Expected: bid, Detail: "file not found in boot/http/" + bid + "/"})
				v.Ready = false
			}
		}
	}

	return v
}

// isMountPointOrHasContent checks if a path is a mount point or contains files.
func isMountPointOrHasContent(path string) bool {
	// Check mount point via /proc/mounts
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == absPath {
				return true
			}
		}
	}
	// Fallback: check if directory exists and has content
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// InheritTemplateResources fills in scripts/files from os_template if not provided in task.
func (s *Store) InheritTemplateResources(req *db.TaskCreate) {
	if req.OS == "" {
		return
	}
	tpl := s.DB.FindOSTemplateByBid(req.OS)
	if tpl == nil {
		return
	}

	// Inherit scripts: resolve from DB and serialize as JSON
	if req.Scripts == "" || req.Scripts == "[]" {
		scripts := s.DB.ResolveScripts(tpl.ScriptBids)
		if len(scripts) > 0 {
			type scriptPayload struct {
				Name    string `json:"name"`
				Type    string `json:"type"`
				Content string `json:"content"`
			}
			var payload []scriptPayload
			for _, sc := range scripts {
				payload = append(payload, scriptPayload{
					Name:    sc.Name,
					Type:    sc.ScriptType,
					Content: sc.Content,
				})
			}
			if data, err := json.Marshal(payload); err == nil {
				req.Scripts = string(data)
			}
		}
	}

	// Inherit files: resolve from DB and serialize as JSON
	if req.Files == "" || req.Files == "[]" {
		fileBids := db.ParseBids(tpl.FileBids)
		if len(fileBids) > 0 {
			type filePayload struct {
				Filename string `json:"filename"`
				URL      string `json:"url"`
				Dest     string `json:"dest"`
			}
			var payload []filePayload
			for _, bid := range fileBids {
				f, err := s.DB.GetFile(bid)
				if err != nil || f == nil {
					continue
				}
				payload = append(payload, filePayload{
					Filename: f.Filename,
					URL:      "/" + f.Path,
					Dest:     f.DestDir + "/" + f.Filename,
				})
			}
			if len(payload) > 0 {
				if data, err := json.Marshal(payload); err == nil {
					req.Files = string(data)
				}
			}
		}
	}
}
