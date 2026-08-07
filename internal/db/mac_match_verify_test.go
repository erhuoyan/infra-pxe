package db

import (
	"os"
	"path/filepath"
	"testing"
)

// Prove /boot/mac/{mac} matching works against the CURRENT schema:
// single-port matches network.mac, bond matches either slave.
func TestMacFallbackMatching(t *testing.T) {
	dir, _ := os.MkdirTemp("", "db-match")
	defer os.RemoveAll(dir)
	d, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 单口：mac 在 network.mac
	if _, err := d.CreateTask(&TaskCreate{
		SN: "SN1", Hostname: "h1", IP: "6.24.150.71",
		Network: `{"ip":"6.24.150.71","netmask":"255.255.255.224","gateway":"6.24.150.65","mac":"5c:6f:69:1c:56:a0"}`,
	}); err != nil {
		t.Fatal(err)
	}
	// bond：slaves 两块口
	if _, err := d.CreateTask(&TaskCreate{
		SN: "SN2", Hostname: "h2", IP: "6.24.150.72",
		Network: `{"ip":"6.24.150.72","netmask":"255.255.255.224","gateway":"6.24.150.65","bond":{"mode":4,"slaves":["5c:6f:69:1c:56:a1","5c:6f:69:1c:56:a2"]}}`,
	}); err != nil {
		t.Fatal(err)
	}

	// 单口：network.mac 必须命中
	if tk, err := d.GetTaskByMAC("5c:6f:69:1c:56:a0"); err != nil || tk.SN != "SN1" {
		t.Fatalf("FAIL 单口 network.mac 未命中: err=%v task=%+v", err, tk)
	} else {
		t.Logf("OK  单口 network.mac → %s", tk.SN)
	}

	// bond：两个 slave 都必须命中
	for _, m := range []string{"5c:6f:69:1c:56:a1", "5c:6f:69:1c:56:a2"} {
		if tk, err := d.GetTaskByMAC(m); err != nil || tk.SN != "SN2" {
			t.Fatalf("FAIL bond slave %s 未命中: err=%v task=%+v", m, err, tk)
		} else {
			t.Logf("OK  bond slave %s → %s", m, tk.SN)
		}
	}

	// 大小写/短横线规范化也要命中
	if tk, err := d.GetTaskByMAC("5C-6F-69-1C-56-A1"); err != nil || tk.SN != "SN2" {
		t.Fatalf("FAIL 规范化 MAC 未命中: err=%v", err)
	} else {
		t.Logf("OK  规范化 5C-6F-69-1C-56-A1 → %s", tk.SN)
	}

	// 不存在的 MAC 必须不命中
	if _, err := d.GetTaskByMAC("aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatal("FAIL 未知 MAC 不应命中")
	} else {
		t.Log("OK  未知 MAC 不命中（返回任务不存在）")
	}
}
