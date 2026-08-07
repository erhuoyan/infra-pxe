package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsEngineSection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "pxe.yaml")
	content := []byte(`engine:
  listen: "127.0.0.2"
  port: 9300
  interface: "ib0"
  name: "pxe-test"
`)
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine.Listen != "127.0.0.2" {
		t.Fatalf("Engine.Listen = %q, want %q", cfg.Engine.Listen, "127.0.0.2")
	}
	if cfg.Engine.Port != 9300 {
		t.Fatalf("Engine.Port = %d, want %d", cfg.Engine.Port, 9300)
	}
	if cfg.Engine.Interface != "ib0" {
		t.Fatalf("Engine.Interface = %q, want %q", cfg.Engine.Interface, "ib0")
	}
	if cfg.Engine.Name != "pxe-test" {
		t.Fatalf("Engine.Name = %q, want %q", cfg.Engine.Name, "pxe-test")
	}
	if got := cfg.ListenAddr(); got != "127.0.0.2" {
		t.Fatalf("ListenAddr() = %q, want %q", got, "127.0.0.2")
	}
}
