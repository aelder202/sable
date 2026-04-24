package agentlabel

import (
	"strings"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	cases := []string{
		"main",
		"web01",
		"a",
		"z9",
		"a-b",
		"a_b",
		"web-01_prod",
		"3b2f9c1a",
		strings.Repeat("a", 31),
	}
	for _, label := range cases {
		if err := Validate(label); err != nil {
			t.Errorf("Validate(%q) returned error %v; want nil", label, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"empty", ""},
		{"uppercase", "Web01"},
		{"starts with dash", "-web"},
		{"starts with underscore", "_web"},
		{"too long", strings.Repeat("a", 32)},
		{"contains slash", "a/b"},
		{"contains backslash", "a\\b"},
		{"contains space", "web 01"},
		{"contains period", "a.b"},
		{"reserved dot", "."},
		{"reserved dotdot", ".."},
		{"reserved builds", "builds"},
		{"reserved agents", "agents"},
		{"reserved config", "config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.label); err == nil {
				t.Errorf("Validate(%q) returned nil; want error", tc.label)
			}
		})
	}
}

func TestFromUUIDPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"3b2f9c1a-4e21-40ff-8a1d-9d4bc1e02f77", "3b2f9c1a"},
		{"abcd1234", "abcd1234"},
		{"no-dashes-here", "no"},
		{"short", "short"},
	}
	for _, tc := range cases {
		if got := FromUUIDPrefix(tc.in); got != tc.want {
			t.Errorf("FromUUIDPrefix(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
