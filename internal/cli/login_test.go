package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

func TestRemedyTextAddsWindowsShellHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	cases := []struct {
		windows bool
		code    string
		want    bool
	}{
		{true, "repo_not_indexed", true},
		{true, "app_no_repo_access", true},
		{true, "auth_required", true},
		{true, "insufficient_credits", false},
		{false, "repo_not_indexed", false},
		{false, "auth_required", false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("windows=%v/%s", c.windows, c.code), func(t *testing.T) {
			previous := separateShellLogins
			separateShellLogins = func() bool { return c.windows }
			t.Cleanup(func() { separateShellLogins = previous })
			text := remedyText(&service.Error{Body: service.ErrorBody{
				Code: c.code, Message: "message", Remedy: "remedy",
			}})
			if !strings.HasPrefix(text, "message\nremedy") {
				t.Fatalf("service text changed: %q", text)
			}
			got := strings.Contains(text, "run `slopcop login` in the terminal the agent uses")
			if got != c.want {
				t.Fatalf("hint present = %v, want %v: %q", got, c.want, text)
			}
			if got && !strings.Contains(text, "config.json") {
				t.Fatalf("hint does not name the config file: %q", text)
			}
		})
	}
}
