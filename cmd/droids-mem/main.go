package main

import (
	"fmt"
	"os"

	"github.com/samuelmolero/droids-mem/internal/db"
	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	conn, err := db.Open()
	if err != nil {
		writeError("db_init_failed", err.Error(), false,
			withSuggestion("check DROIDS_MEM_DB env var or ensure ~/.droids-mem/ is writable"),
		)
		os.Exit(ExitError)
	}
	defer conn.Close()

	s := store.New(conn)

	root := &cobra.Command{
		Use:   "droids-mem",
		Short: "Persistent memory tool for AI agents",
		Long: `droids-mem gives AI agents a persistent memory layer backed by SQLite + FTS5.

Agents save structured lessons, search past lessons, and load context at the
start of each run — all via a local binary with zero external dependencies.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newSaveCmd(s),
		newSearchCmd(s),
		newContextCmd(s),
		newListCmd(s),
		newGetCmd(s),
		newDoctorCmd(s),
		newSchemaCmd(),
		newServeCmd(s),
		newEnsureServerCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(ExitUsage)
	}
}

// exitWith terminates the process with the given exit code.
// Used by command handlers after writing their output.
func exitWith(code int) {
	os.Exit(code)
}
