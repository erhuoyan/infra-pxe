// infra-pxe: self-contained PXE Engine.
// Single static binary with SQLite — complete install lifecycle via REST API.
package main

import (
	"log/slog"
	"os"

	"github.com/joyops/infra-pxe/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		slog.Error("command failed", "err", err)
		os.Exit(1)
	}
}
