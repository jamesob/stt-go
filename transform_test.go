package main

import "testing"

func TestTransformText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"slash help", "/help"},
		{"Slash help me out", "/help me out"},
		{"slash  compact", "/compact"},
		{"backslash help", "backslash help"},
		{"say slash help", "say slash help"},
		{"hello world", "hello world"},
		{"slash", "slash"}, // no trailing word, unchanged
		{"", ""},
	}
	for _, c := range cases {
		if got := transformText(c.in); got != c.want {
			t.Errorf("transformText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
