package runthrough

import "net/url"

// RedactEndpoint returns only the host (and optional port) from an upstream
// endpoint. Paths, queries, fragments, and user information are never exposed.
func RedactEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}
