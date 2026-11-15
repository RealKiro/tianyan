package teacher

import "testing"

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"01号机":    "01号机",
		"张三/01":   "张三-01",
		"a b:c*d": "a-b-c-d",
		"PC-01":   "PC-01",
		"???":     "student",
		"":        "student",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
