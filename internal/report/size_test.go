package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/reposize"
	"github.com/BrokkAi/slopcop/internal/service"
)

func nearLimit() *service.RepositorySize {
	return &service.RepositorySize{
		Files: 12_000, Bytes: 460 << 20, MaxFiles: 50_000, MaxBytes: 512 << 20,
		ExcludedFiles: 3, ExcludedBytes: 2 << 20, NearLimit: true,
	}
}

func sampleBreakdown() *reposize.Breakdown {
	return &reposize.Breakdown{
		Base: "0123456789abcdef0123456789abcdef01234567", Files: 11_990, Bytes: 700 << 20,
		ExcludedFiles: 3, ExcludedBytes: 2 << 20, RankedBy: reposize.RankedByBytes,
		Largest: []reposize.Entry{
			{Path: ".agents/records", Directory: true, Files: 9_000, Bytes: 666 << 20, Pattern: ".agents/records/**"},
			{Path: "src", Directory: true, Files: 2_000, Bytes: 30 << 20, Pattern: "src/**"},
			{Path: "dump.sql", Files: 1, Bytes: 4 << 20, Pattern: "/dump.sql"},
		},
	}
}

func TestSizeAdviceNamesTheLargestPathsAndAReadySnippet(t *testing.T) {
	got := SizeAdvice(nearLimit(), sampleBreakdown())
	want := "Repository near the size limit: the check's checkout held 12,000 of 50,000 files and 460.0 MiB of 512.0 MiB.\n" +
		"Base tree 0123456789ab: 11,990 file(s), 700.0 MiB after .bifrostignore and [check] exclude leave out 3 file(s), 2.0 MiB. Largest paths by size:\n" +
		"  .agents/records/   666.0 MiB  9,000 file(s)\n" +
		"  src/                30.0 MiB  2,000 file(s)\n" +
		"  dump.sql             4.0 MiB  1 file(s)\n" +
		"Keep the generated, vendored or data paths among them out of every check in .slopcop/config.toml, for example:\n" +
		"  [check]\n" +
		"  exclude = [\".agents/records/**\"]\n" +
		"Or talk to us about an enterprise agreement for higher limits.\n"
	if got != want {
		t.Fatalf("advice =\n%s\nwant\n%s", got, want)
	}
}

func TestSizeAdviceAddsToAnExistingCheckSection(t *testing.T) {
	breakdown := sampleBreakdown()
	breakdown.HasCheck = true
	breakdown.Exclude = []string{"vendor/**", `odd"name`}
	breakdown.RankedBy = reposize.RankedByFiles
	got := SizeAdvice(nil, breakdown)
	if strings.Contains(got, "near the size limit") || strings.Contains(got, "[check]\n") {
		t.Fatalf("advice = %s", got)
	}
	for _, want := range []string{
		"Largest paths by file count:",
		"in the [check] section of .slopcop/config.toml, for example:\n  exclude = [\"vendor/**\", \"odd\\\"name\", \".agents/records/**\"]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("advice = %s\nwant %q", got, want)
		}
	}
}

func TestSizeAdviceWithoutABreakdownStillSaysWhatToDo(t *testing.T) {
	got := SizeAdvice(nearLimit(), nil)
	if !strings.Contains(got, "[check] exclude list in .slopcop/config.toml") || !strings.HasSuffix(got, enterpriseLine) {
		t.Fatalf("advice = %s", got)
	}
	breakdown := sampleBreakdown()
	breakdown.ConfigError = ".slopcop/config.toml does not parse: bad\x1b[31m"
	if got := SizeAdvice(nil, breakdown); !strings.Contains(got, "Note: .slopcop/config.toml does not parse: bad") || strings.Contains(got, "\x1b") {
		t.Fatalf("advice = %q", got)
	}
}

func TestHumanReportWarnsOnlyNearTheLimit(t *testing.T) {
	near := response(service.StatusClean, "finding", nil)
	near.RepositorySize = nearLimit()
	if got := renderWith(t, near, FormatHuman, Options{SizeBreakdown: sampleBreakdown()}); !strings.Contains(got, "Repository near the size limit") || !strings.Contains(got, ".agents/records/") {
		t.Fatalf("near = %s", got)
	}
	far := response(service.StatusClean, "finding", nil)
	far.RepositorySize = nearLimit()
	far.RepositorySize.NearLimit = false
	if got := render(t, far, FormatHuman); strings.Contains(got, "size limit") {
		t.Fatalf("far = %s", got)
	}
}

func TestMachineReportsCarryTheSizeUnchanged(t *testing.T) {
	near := response(service.StatusClean, "finding", nil)
	near.RepositorySize = nearLimit()
	var document struct {
		RepositorySize map[string]any      `json:"repositorySize"`
		SizeBreakdown  *reposize.Breakdown `json:"sizeBreakdown"`
	}
	if err := json.Unmarshal([]byte(renderWith(t, near, FormatJSON, Options{SizeBreakdown: sampleBreakdown()})), &document); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"files": float64(12_000), "bytes": float64(460 << 20), "maxFiles": float64(50_000), "maxBytes": float64(512 << 20),
		"excludedFiles": float64(3), "excludedBytes": float64(2 << 20), "nearLimit": true,
	}
	if len(document.RepositorySize) != len(want) {
		t.Fatalf("repositorySize = %v", document.RepositorySize)
	}
	for key, value := range want {
		if document.RepositorySize[key] != value {
			t.Errorf("repositorySize.%s = %v want %v", key, document.RepositorySize[key], value)
		}
	}
	if document.SizeBreakdown == nil || document.SizeBreakdown.Largest[0].Pattern != ".agents/records/**" {
		t.Fatalf("sizeBreakdown = %+v", document.SizeBreakdown)
	}
	plain := render(t, response(service.StatusClean, "finding", nil), FormatJSON)
	if strings.Contains(plain, "repositorySize") || strings.Contains(plain, "sizeBreakdown") {
		t.Fatalf("an old service's answer gained size fields: %s", plain)
	}
	sarif := renderWith(t, near, FormatSARIF, Options{SizeBreakdown: sampleBreakdown()})
	if !strings.Contains(sarif, `"slopcop.repositorySize"`) || !strings.Contains(sarif, `"slopcop.sizeBreakdown"`) {
		t.Fatalf("sarif = %s", sarif)
	}
}

func TestFailureReportCarriesTheBreakdown(t *testing.T) {
	var out bytes.Buffer
	failure := &service.Error{StatusCode: 422, Body: service.ErrorBody{Code: "repository_too_large", Message: "too large", Remedy: "exclude"}}
	if err := WriteFailure(&out, failure, FormatJSON, Options{SizeBreakdown: sampleBreakdown()}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Code          string              `json:"code"`
		SizeBreakdown *reposize.Breakdown `json:"sizeBreakdown"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Code != "repository_too_large" || document.SizeBreakdown == nil || len(document.SizeBreakdown.Largest) != 3 {
		t.Fatalf("failure = %s", out.String())
	}
}

func TestBytesAndCount(t *testing.T) {
	for value, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 512 << 20: "512.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := Bytes(value); got != want {
			t.Errorf("Bytes(%d) = %q want %q", value, got, want)
		}
	}
	for value, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := count(value); got != want {
			t.Errorf("count(%d) = %q want %q", value, got, want)
		}
	}
}
