package main

import "github.com/spf13/cobra"

var schemaDefinitions = map[string]any{
	"save": map[string]any{
		"command":     "save",
		"description": "Save a structured memory",
		"flags": []map[string]any{
			{"name": "task-type", "type": "string", "required": true, "description": "Task type identifier, e.g. crm_upload"},
			{"name": "kind", "type": "enum", "required": true, "values": []string{"error_resolution", "task_pattern", "user_rule", "session_summary"}},
			{"name": "title", "type": "string", "required": true, "description": "Short title for the memory"},
			{"name": "what", "type": "string", "required": true, "description": "What happened"},
			{"name": "learned", "type": "string", "required": true, "description": "What to do next time"},
			{"name": "tags", "type": "string", "required": false, "description": "Space-delimited tags"},
			{"name": "session-id", "type": "string", "required": false, "description": "Group saves in one run (auto-generated if omitted)"},
			{"name": "force", "type": "bool", "required": false, "default": false, "description": "Overwrite existing memory (HITL correction)"},
			{"name": "dry-run", "type": "bool", "required": false, "default": false, "description": "Preview without writing, exits 10"},
		},
	},
	"search": map[string]any{
		"command":     "search",
		"description": "Full-text search over stored memories",
		"flags": []map[string]any{
			{"name": "query", "type": "string", "required": true, "description": "Search terms"},
			{"name": "task-type", "type": "string", "required": false, "description": "Filter by task type"},
			{"name": "kind", "type": "enum", "required": false, "values": []string{"error_resolution", "task_pattern", "user_rule", "session_summary"}},
			{"name": "limit", "type": "int", "required": false, "default": 5, "max": 20},
		},
	},
	"context": map[string]any{
		"command":     "context",
		"description": "Load start-of-run context bundle (two-tier: always + browse)",
		"flags": []map[string]any{
			{"name": "task-type", "type": "string", "required": true, "description": "Task type to load context for"},
			{"name": "query", "type": "string", "required": false, "description": "FTS query for browse-tier ranking (defaults to task-type tokens)"},
		},
		"response": map[string]any{
			"task_type":    "string",
			"last_session": "ContextMemory? (always tier — full body)",
			"user_rules":   "[]ContextMemory (always tier — full body, all rules for task_type)",
			"browse":       "[]ContextMemory (browse tier — title + 120-char snippet of `what`; use `get --id` for full body)",
		},
	},
	"list": map[string]any{
		"command":     "list",
		"description": "List recent memories",
		"flags": []map[string]any{
			{"name": "task-type", "type": "string", "required": false},
			{"name": "kind", "type": "enum", "required": false, "values": []string{"error_resolution", "task_pattern", "user_rule", "session_summary"}},
			{"name": "limit", "type": "int", "required": false, "default": 20, "max": 100},
		},
	},
	"get": map[string]any{
		"command":     "get",
		"description": "Get a single memory by ID",
		"flags": []map[string]any{
			{"name": "id", "type": "string", "required": true, "description": "Memory ID with mem_ prefix"},
		},
	},
	"doctor": map[string]any{
		"command":     "doctor",
		"description": "Check FTS integrity, rebuild if divergent, optimize, VACUUM",
		"flags":       []map[string]any{},
		"response": map[string]any{
			"status":          "string",
			"integrity_ok":    "bool",
			"rebuilt":         "bool",
			"optimized":       "bool",
			"vacuumed":        "bool",
			"bytes_before":    "int64",
			"bytes_after":     "int64",
			"bytes_freed":     "int64",
			"integrity_error": "string? (present when integrity_ok=false)",
		},
	},
}

func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [command]",
		Short: "Show parameter schema for a command (or all commands)",
		Example: `  droids-mem schema
  droids-mem schema save
  droids-mem schema search`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				writeJSON(schemaDefinitions)
				return nil
			}
			def, ok := schemaDefinitions[args[0]]
			if !ok {
				writeError("not_found", "no schema for command "+args[0], false,
					withSuggestion("run 'droids-mem schema' to list all commands"),
				)
				exitWith(ExitNotFound)
			}
			writeJSON(def)
			return nil
		},
	}
}
