package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

const npmBinary = "/usr/local/lib/node_modules/@brokkai/slopcop/bin/slopcop"

// fakeSource answers latest with tag, or fails when tag is "", and records
// each apply.
func fakeSource(executable, tag string, applied *[]string) updateSource {
	return updateSource{
		executable: executable,
		latest: func(context.Context) (string, error) {
			if tag == "" {
				return "", errors.New("offline")
			}
			return tag, nil
		},
		apply: func(_ context.Context, tag, _ string) error {
			*applied = append(*applied, tag)
			return nil
		},
	}
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var exit silentExit
	if errors.As(err, &exit) {
		return exit.code
	}
	return 2
}

func TestUpdateChecksTheLatestReleaseForEveryChannel(t *testing.T) {
	cases := []struct {
		name       string
		executable string
		version    string
		latest     string
		check      bool
		wantOut    string
		wantExit   int
		wantApply  bool
	}{
		{"npm up to date", npmBinary, "v0.0.28", "v0.0.28", false,
			"slopcop: v0.0.28 is up to date (latest release v0.0.28)\n", 0, false},
		{"npm behind", npmBinary, "v0.0.27", "v0.0.28", false,
			"slopcop: v0.0.27 → v0.0.28 available. Upgrade: npm install --global @brokkai/slopcop@latest\n", 0, false},
		{"npm behind, --check", npmBinary, "v0.0.27", "v0.0.28", true,
			"slopcop: v0.0.27 → v0.0.28 available. Upgrade: npm install --global @brokkai/slopcop@latest\n", updateAvailable, false},
		{"npm up to date, --check", npmBinary, "v0.0.28", "v0.0.28", true,
			"slopcop: v0.0.28 is up to date (latest release v0.0.28)\n", 0, false},
		{"self-managed behind", "/home/me/.local/bin/slopcop", "v0.0.27", "v0.0.28", false,
			"slopcop: updated from v0.0.27 to v0.0.28\n", 0, true},
		{"self-managed behind, --check", "/home/me/.local/bin/slopcop", "v0.0.27", "v0.0.28", true,
			"slopcop: v0.0.27 → v0.0.28 available. Upgrade: slopcop update\n", updateAvailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			var applied []string
			err := runUpdate(context.Background(), &out, tc.version, tc.check, fakeSource(tc.executable, tc.latest, &applied))
			if got := exitOf(err); got != tc.wantExit {
				t.Fatalf("exit = %d (err %v), want %d", got, err, tc.wantExit)
			}
			if out.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
			if (len(applied) > 0) != tc.wantApply {
				t.Errorf("applied = %v, want apply %v", applied, tc.wantApply)
			}
		})
	}
}

func TestUpdateFailsWhenTheLatestReleaseCannotBeRead(t *testing.T) {
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, "v0.0.27", true, fakeSource(npmBinary, "", &applied))
	if err == nil || exitOf(err) != 2 {
		t.Fatalf("err = %v, want a failure that exits 2", err)
	}
	// Without --check, a package-managed install still learns how to upgrade.
	err = runUpdate(context.Background(), &out, "v0.0.27", false, fakeSource(npmBinary, "", &applied))
	if err == nil || err.Error() != "offline. Upgrade: npm install --global @brokkai/slopcop@latest" {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 || len(applied) != 0 {
		t.Errorf("stdout = %q, applied = %v", out.String(), applied)
	}
}

func TestUpdateLeavesADevBuildAlone(t *testing.T) {
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, "dev", false, fakeSource(npmBinary, "v0.0.28", &applied))
	if err != nil || out.String() != "slopcop: dev is not a release build, so it is left as it is\n" {
		t.Errorf("err = %v, stdout = %q", err, out.String())
	}
	// --check cannot say up to date or behind, so it fails rather than pass.
	out.Reset()
	err = runUpdate(context.Background(), &out, "dev", true, fakeSource(npmBinary, "v0.0.28", &applied))
	if exitOf(err) != 2 || out.Len() != 0 || len(applied) != 0 {
		t.Errorf("err = %v, stdout = %q, applied = %v", err, out.String(), applied)
	}
}
