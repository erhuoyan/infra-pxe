package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/store"
)

// Seed yaml row shapes — only fields we consume.
type isoSourceRow struct {
	Bid          string `yaml:"bid"`
	DistroFamily string `yaml:"distro_family"`
	Version      string `yaml:"version"`
	Variant      string `yaml:"variant"`
	Arch         string `yaml:"arch"`
	ISOPath      string `yaml:"iso_path"`
}

type osTemplateRow struct {
	Bid        string `yaml:"bid"`
	Label      string `yaml:"label"`
	ISOBid     string `yaml:"iso_bid"`
	BootType   string `yaml:"boot_type"`
	Template   string `yaml:"template"`
	KernelArgs string `yaml:"kernel_args"`
	MirrorURL  string `yaml:"mirror_url"`
	ScriptBids string `yaml:"script_bids"`
	FileBids   string `yaml:"file_bids"`
}

type importResult struct {
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

type seedImportResponse struct {
	Templates importResult `json:"templates"`
	OS        importResult `json:"os"`
	SeedsDir  string       `json:"seeds_dir"`
	TplDir    string       `json:"templates_dir"`
}

// seedsDir resolves the packaged seed yaml directory (sibling of templates_dir).
func seedsDir(cfg *config.Config) string {
	if cfg.Paths.TemplatesDir != "" {
		return filepath.Join(filepath.Dir(cfg.TemplatesDir()), "seeds")
	}
	return filepath.Join(cfg.BaseDir, "seeds")
}

// POST /api/seed/import?overwrite=true — import bundled seeds into the DB.
//
// Reads:
//   - <TemplatesDir>/*.j2 and <TemplatesDir>/scripts/* (file-based; presence check)
//   - <SeedsDir>/iso_sources.yaml + os_templates.yaml → os_templates table
//
// Idempotent. Default skips existing os_templates; overwrite=true forces update.
// Existing rows are left alone unless overwrite=true.
func seedImportHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		overwrite := r.URL.Query().Get("overwrite") == "true"

		resp := seedImportResponse{
			TplDir:   cfg.TemplatesDir(),
			SeedsDir: seedsDir(cfg),
		}

		countTemplates(resp.TplDir, &resp.Templates)
		importOSTemplates(s, resp.SeedsDir, overwrite, &resp.OS)

		jsonOK(w, resp)
	}
}

// countTemplates reports how many .j2 + scripts/* are present on disk.
// Templates are file-based and land here at install time; this only verifies presence.
func countTemplates(tplDir string, r *importResult) {
	entries, err := os.ReadDir(tplDir)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("templates dir: %v", err))
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".j2") {
			r.Added++
		}
	}
	if scEntries, err := os.ReadDir(filepath.Join(tplDir, "scripts")); err == nil {
		for _, e := range scEntries {
			if !e.IsDir() {
				r.Added++
			}
		}
	}
}

func importOSTemplates(s *store.Store, seedDir string, overwrite bool, r *importResult) {
	isoRows, err := readYAML[isoSourceRow](filepath.Join(seedDir, "iso_sources.yaml"))
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	osRows, err := readYAML[osTemplateRow](filepath.Join(seedDir, "os_templates.yaml"))
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}

	type isoInfo struct {
		DistroPath   string
		DistroFamily string
		ISOPath      string
	}
	isoMap := make(map[string]isoInfo, len(isoRows))
	for _, iso := range isoRows {
		variant := iso.Variant
		if variant == "" {
			variant = "standard"
		}
		isoMap[iso.Bid] = isoInfo{
			DistroPath:   fmt.Sprintf("%s/%s/%s/%s", iso.DistroFamily, iso.Version, variant, iso.Arch),
			DistroFamily: iso.DistroFamily,
			ISOPath:      iso.ISOPath,
		}
	}

	for _, row := range osRows {
		if row.Bid == "" {
			r.Errors = append(r.Errors, "os_templates.yaml row missing bid")
			continue
		}
		info := isoMap[row.ISOBid]
		bootType := row.BootType
		if bootType == "" {
			bootType = "kickstart"
		}
		tpl := &db.OSTemplate{
			Bid:          row.Bid,
			Label:        row.Label,
			DistroPath:   info.DistroPath,
			DistroFamily: info.DistroFamily,
			BootType:     bootType,
			KernelArgs:   row.KernelArgs,
			Template:     row.Template,
			ISOPath:      info.ISOPath,
			MirrorURL:    row.MirrorURL,
			ScriptBids:   row.ScriptBids,
			FileBids:     row.FileBids,
		}

		existing, _ := s.DB.GetOSTemplate(row.Bid)
		if existing != nil && !overwrite {
			r.Skipped++
			continue
		}
		if err := s.DB.UpsertOSTemplate(tpl); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", row.Bid, err))
			continue
		}
		if existing != nil {
			r.Updated++
		} else {
			r.Added++
		}
	}
}

func readYAML[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rows []T
	if err := yaml.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return rows, nil
}
