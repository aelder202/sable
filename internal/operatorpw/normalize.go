// Package operatorpw normalizes operator password file contents so the server
// and sablectl agree on what the password is, even when the file was written
// with a UTF-16 BOM (PowerShell's default) or other Windows-friendly encodings.
package operatorpw

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Normalize returns the trimmed, decoded password from the raw bytes of a
// password file. It strips UTF-8/UTF-16 BOMs and decodes UTF-16 little- or
// big-endian content. Plain UTF-8 ASCII files are returned trimmed.
//
// Order matters: BOMs are unambiguous and checked first. BOM-less UTF-16 is
// detected by null-byte heuristics before the utf8.Valid branch, because
// `s\x00e\x00...` is technically valid UTF-8 too — checking utf8.Valid first
// would mis-decode UTF-16 LE files written without a BOM.
func Normalize(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return strings.TrimSpace(string(data[3:]))
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return strings.TrimSpace(decodeUTF16(data[2:], binary.LittleEndian))
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return strings.TrimSpace(decodeUTF16(data[2:], binary.BigEndian))
	case looksLikeUTF16(data, 1):
		return strings.TrimSpace(decodeUTF16(data, binary.LittleEndian))
	case looksLikeUTF16(data, 0):
		return strings.TrimSpace(decodeUTF16(data, binary.BigEndian))
	case utf8.Valid(data):
		return strings.TrimSpace(string(data))
	default:
		return strings.TrimSpace(string(data))
	}
}

func decodeUTF16(data []byte, order binary.ByteOrder) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	words := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		words = append(words, order.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(words))
}

func looksLikeUTF16(data []byte, nullByteIndex int) bool {
	if len(data) < 4 || len(data)%2 != 0 {
		return false
	}
	nulls := 0
	pairs := 0
	for i := nullByteIndex; i < len(data); i += 2 {
		pairs++
		if data[i] == 0 {
			nulls++
		}
	}
	return pairs > 0 && nulls*2 >= pairs
}
