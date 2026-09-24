package runthrough_test

import (
	"testing"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestRedactEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://user:secret@example.com:443/path?token=x#fragment": "example.com:443",
		"https://example.com/path":                                  "example.com",
		"not a url":                                                 "",
	}
	for input, want := range tests {
		if got := runthrough.RedactEndpoint(input); got != want {
			t.Errorf("RedactEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
