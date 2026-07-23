package ui

import "testing"

func TestPlural(t *testing.T) {
	cases := []struct {
		n              int
		singular, form string
		wantWord       string
		want           string
	}{
		{0, "conflict", "", "conflicts", "0 conflicts"},
		{1, "conflict", "", "conflict", "1 conflict"},
		{2, "conflict", "", "conflicts", "2 conflicts"},
		// Irregulars pass an explicit plural form.
		{1, "file copy", "file copies", "file copy", "1 file copy"},
		{3, "file copy", "file copies", "file copies", "3 file copies"},
	}
	for _, c := range cases {
		if got := pluralWord(c.n, c.singular, c.form); got != c.wantWord {
			t.Errorf("pluralWord(%d, %q, %q) = %q, want %q", c.n, c.singular, c.form, got, c.wantWord)
		}
		if got := plural(c.n, c.singular, c.form); got != c.want {
			t.Errorf("plural(%d, %q, %q) = %q, want %q", c.n, c.singular, c.form, got, c.want)
		}
	}
}
