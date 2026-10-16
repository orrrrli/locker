package domain

import "testing"

func TestInitials(t *testing.T) {
	for name, want := range map[string]string{
		"Atlético Narvarte":         "AN",
		"Pumas":                     "PU",
		"  los   avengers legends ": "LA",
		"Ñandúes FC":                "ÑF",
		"Real 1987":                 "R1",
		"A":                         "A",
		"Club. Halcones-Sur":        "CH",
		"":                          "",
	} {
		if got := Initials(name); got != want {
			t.Errorf("Initials(%q) = %q, want %q", name, got, want)
		}
	}
}
