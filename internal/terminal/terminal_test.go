package terminal

import "testing"

func TestSafe(t *testing.T) {
	cases := map[string]string{
		"plain text, é and 漢字": "plain text, é and 漢字",
		"\x1b[31mred\x1b[0m":   "�[31mred�[0m",
		"a\nb\tc":              "a�b�c",
		"x‮y⁦z⁩":               "x�y�z�",
		"bad \xff byte":        "bad � byte",
	}
	for in, want := range cases {
		if got := Safe(in); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
	}
}
