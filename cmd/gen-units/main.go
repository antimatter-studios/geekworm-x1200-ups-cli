// Command gen-units writes the packaged copies of the systemd units.
//
// Run by `go generate ./...` and by `make units`. The generated files are checked in so that
// packaging does not need a Go toolchain, and a test compares them against this generator so the
// two cannot drift.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/service"
)

func main() {
	out := flag.String("out", "packaging", "directory to write the units into")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gen-units:", err)
		os.Exit(1)
	}
	files := service.Files(service.DefaultBinary)
	for _, name := range service.Names() {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen-units:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}
