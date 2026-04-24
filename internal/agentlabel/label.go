// Package agentlabel validates and generates human-readable agent labels.
// Labels name filesystem paths (agents/<label>.env, builds/<label>/…), so
// they must be safe on every supported operating system.
package agentlabel

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Lowercase-only avoids Windows case-insensitive filesystem collisions.
var labelRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// Reserved labels would collide with repo directory names or break filesystem
// tooling. Belt-and-suspenders on top of the regex.
var reserved = map[string]struct{}{
	".":      {},
	"..":     {},
	"builds": {},
	"agents": {},
	"config": {},
}

// ErrEmpty is returned when the label is an empty string.
var ErrEmpty = errors.New("label is empty")

// Validate returns nil if label is well-formed and not reserved.
func Validate(label string) error {
	if label == "" {
		return ErrEmpty
	}
	if !labelRegex.MatchString(label) {
		return fmt.Errorf("label %q must match %s", label, labelRegex.String())
	}
	if _, ok := reserved[label]; ok {
		return fmt.Errorf("label %q is reserved", label)
	}
	return nil
}

// FromUUIDPrefix returns a short label derived from the first hyphen-separated
// chunk of a UUID (8 hex chars for a standard UUID). Falls back to the first
// 8 chars if no hyphen is present, or the whole string if shorter than 8.
func FromUUIDPrefix(uuid string) string {
	if idx := strings.IndexByte(uuid, '-'); idx > 0 {
		return uuid[:idx]
	}
	if len(uuid) >= 8 {
		return uuid[:8]
	}
	return uuid
}
