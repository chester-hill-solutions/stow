package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

func canonicalURIPath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = uriEncode(segment, false)
	}
	return strings.Join(segments, "/")
}

func uriEncode(value string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		if c == '/' && !encodeSlash {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func canonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	deleteCaseInsensitive(values, "X-Amz-Signature")

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		vals := values[key]
		sort.Strings(vals)
		encodedKey := uriEncode(key, true)
		for _, val := range vals {
			parts = append(parts, encodedKey+"="+uriEncode(val, true))
		}
	}
	return strings.Join(parts, "&")
}

func deleteCaseInsensitive(values url.Values, target string) {
	for key := range values {
		if strings.EqualFold(key, target) {
			delete(values, key)
		}
	}
}

func canonicalHeaders(r *http.Request, signed []string) (string, string, error) {
	headerMap := make(map[string]string, len(signed))
	for _, name := range signed {
		value, err := signedHeaderValue(r, name)
		if err != nil {
			return "", "", err
		}
		headerMap[name] = normalizeHeaderValue(value)
	}

	ordered := make([]string, len(signed))
	copy(ordered, signed)
	sort.Strings(ordered)

	var b strings.Builder
	for _, name := range ordered {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(headerMap[name])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(ordered, ";"), nil
}

func signedHeaderValue(r *http.Request, name string) (string, error) {
	switch name {
	case "host":
		host := headerValue(r.Header, "Host")
		if host == "" {
			host = r.Host
		}
		if host == "" {
			return "", authError("AccessDenied", "missing host header")
		}
		return host, nil
	default:
		value := headerValue(r.Header, name)
		if value == "" {
			return "", authError("AccessDenied", "missing signed header %q", name)
		}
		return value, nil
	}
}

func normalizeHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
