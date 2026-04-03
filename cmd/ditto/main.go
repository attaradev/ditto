// Command ditto is the main entry point for the ditto CLI.
package main

import (
	"fmt"
	"os"

	"github.com/attaradev/ditto/cmd"
	"github.com/attaradev/ditto/internal/version"

	// Blank imports register engines with the engine registry at startup.
	_ "github.com/attaradev/ditto/engine/mariadb"
	_ "github.com/attaradev/ditto/engine/postgres"
)

func main() {
	root := cmd.NewRootCmd()
	root.Version = fmt.Sprintf("%s (commit %s, built %s)", version.Version, version.Commit, version.Date)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
