package locales

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTranslationsCoverEverySourceMessage fails on a message a translation
// lacks. `mygo i18n check` only warns about those, and at runtime they fall
// back to English silently, which hides the gap in a translated UI.
func TestTranslationsCoverEverySourceMessage(t *testing.T) {
	source := messageKeys(t, "en.json")
	files, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("expected the source catalog and at least one translation, found %v", files)
	}
	for _, file := range files {
		if file == "en.json" {
			continue
		}
		have := messageKeys(t, file)
		for _, key := range source {
			if !slices.Contains(have, key) {
				t.Errorf("%s lacks %q", file, key)
			}
		}
	}
}

func messageKeys(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var keys []string
	for key := range entries {
		// Keys starting with @ hold metadata, not messages.
		if !strings.HasPrefix(key, "@") {
			keys = append(keys, key)
		}
	}
	return keys
}
