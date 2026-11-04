package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSaveLoadReviewConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	cfg := Config{Token: "kept", Mode: "ai", Review: &Review{Count: 3, Timeout: "5m", Adapters: map[string]Adapter{"terra": {Command: []string{"runner", "--x"}}}}}
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != cfg.Token || got.Mode != cfg.Mode || got.Review == nil || got.Review.Count != 3 || got.Review.Timeout != "5m" || got.Review.Adapters["terra"].Command[1] != "--x" {
		t.Fatalf("loaded config = %+v", got)
	}
	cfg.Review = &Review{}
	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["review"]; ok || !strings.Contains(string(b), `"token": "kept"`) {
		t.Fatalf("saved empty review/token incorrectly: %s", b)
	}
}
