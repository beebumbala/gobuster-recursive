package gobusterdir

import (
	"net/url"
	"testing"
)

func TestDirectoryRedirectStaysOnOriginalOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		candidate string
		location  string
		want      bool
	}{
		{"same origin", "https://example.com/static/", "https://example.com/static/", true},
		{"advertised HTTP", "https://example.com/static/", "http://example.com/static/", true},
		{"explicit default ports", "https://example.com:443/static/", "http://example.com:80/static/", true},
		{"same custom port", "https://example.com:8443/static/", "http://example.com:8443/static/", true},
		{"other port", "https://example.com/static/", "http://example.com:8080/static/", false},
		{"other host", "https://example.com/static/", "http://other.example/static/", false},
		{"other scheme", "https://example.com/static/", "ftp://example.com/static/", false},
		{"login", "https://example.com/static/", "http://example.com/login/", false},
		{"query changed", "https://example.com/static/", "http://example.com/static/?next=login", false},
		{"query preserved", "https://example.com/static/?lang=en", "http://example.com/static/?lang=en", true},
		{"userinfo", "https://example.com/static/", "http://user:password@example.com/static/", false},
		{"no slash", "https://example.com/static/", "http://example.com/static", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate, err := url.Parse(test.candidate)
			if err != nil {
				t.Fatal(err)
			}
			location, err := url.Parse(test.location)
			if err != nil {
				t.Fatal(err)
			}
			if got := directoryRedirect(*location, *candidate); got != test.want {
				t.Fatalf("directoryRedirect(%s, %s) = %t, want %t", test.location, test.candidate, got, test.want)
			}
		})
	}
}
