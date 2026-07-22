package main

import (
	"github.com/spf13/cobra"
)

func newArchiveCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Inspect superseded memories in the archive (ADR-0030)",
		Long: `Supersede retires a memory by archiving it (soft delete) rather than
hard-deleting it. Archived memories are invisible to every retrieval path;
this operator view lets a human audit what was superseded.`,
	}
	cmd.AddCommand(newArchiveListCmd(a))
	return cmd
}

func newArchiveListCmd(a *app) *cobra.Command {
	var taskType string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List archived (superseded) memories, newest first",
		Example: `  droids-mem archive list
  droids-mem archive list --task-type crm_upload`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			resp, err := s.ArchiveList(cmd.Context(), taskType)
			if err != nil {
				writeError("archive_list_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}
	cmd.Flags().StringVar(&taskType, "task-type", "", "Only archived memories of this task_type.")
	return cmd
}
