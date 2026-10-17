package main

import (
	"strings"
	"testing"
)

// TestSchema_SaveScopeDefaultsToPersonal guards issue #210: the machine-readable
// `schema save` output must report the real default ('personal').
func TestSchema_SaveScopeDefaultsToPersonal(t *testing.T) {
	saveDef, ok := schemaDefinitions["save"].(map[string]any)
	if !ok {
		t.Fatal("schemaDefinitions[\"save\"] has unexpected shape")
	}
	flags, ok := saveDef["flags"].([]map[string]any)
	if !ok {
		t.Fatal("save flags have unexpected shape")
	}
	var scopeFlag map[string]any
	for _, f := range flags {
		if f["name"] == "scope" {
			scopeFlag = f
			break
		}
	}
	if scopeFlag == nil {
		t.Fatal("save schema has no scope flag")
	}
	if def := scopeFlag["default"]; def != "personal" {
		t.Errorf("schema save scope default = %v, want personal", def)
	}
}

// TestSaveCmd_ScopeHelpNamesPersonalDefault guards issue #210: `save --scope`
// help must document 'personal' as the default.
func TestSaveCmd_ScopeHelpNamesPersonalDefault(t *testing.T) {
	flag := newSaveCmd(nil).Flags().Lookup("scope")
	if flag == nil {
		t.Fatal("save cmd has no --scope flag")
	}
	if !strings.Contains(flag.Usage, "personal (default)") {
		t.Errorf("--scope usage must document 'personal (default)', got: %q", flag.Usage)
	}
	if strings.Contains(flag.Usage, "shared (default)") {
		t.Errorf("--scope usage still claims 'shared (default)': %q", flag.Usage)
	}
}
