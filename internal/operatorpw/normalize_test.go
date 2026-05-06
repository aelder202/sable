package operatorpw

import "testing"

func TestNormalizeHandlesEncodings(t *testing.T) {
	want := "secret123"
	cases := map[string][]byte{
		"plain ascii":             []byte("secret123"),
		"trailing newline":        []byte("secret123\n"),
		"trailing crlf":           []byte("secret123\r\n"),
		"utf-8 bom":               append([]byte{0xEF, 0xBB, 0xBF}, []byte("secret123")...),
		"utf-16 le bom":           {0xFF, 0xFE, 's', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, '1', 0, '2', 0, '3', 0},
		"utf-16 be bom":           {0xFE, 0xFF, 0, 's', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, '1', 0, '2', 0, '3'},
		"utf-16 le no bom":        {'s', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, '1', 0, '2', 0, '3', 0},
		"utf-16 le with crlf":     {0xFF, 0xFE, 's', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, '1', 0, '2', 0, '3', 0, '\r', 0, '\n', 0},
		"leading/trailing spaces": []byte("  secret123  "),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got := Normalize(data)
			if got != want {
				t.Fatalf("Normalize(%q) = %q, want %q", data, got, want)
			}
		})
	}
}

func TestNormalizeEmpty(t *testing.T) {
	if got := Normalize(nil); got != "" {
		t.Fatalf("Normalize(nil) = %q, want empty", got)
	}
	if got := Normalize([]byte("   \r\n\t  ")); got != "" {
		t.Fatalf("whitespace-only = %q, want empty", got)
	}
}
