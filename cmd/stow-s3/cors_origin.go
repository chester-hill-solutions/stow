package main

import "strings"

// corsOriginList collects repeated --cors-origin values.
//
// A repeatable flag rather than a comma-separated one, because an origin cannot
// contain a comma and splitting on one would corrupt it.
type corsOriginList []string

func (l *corsOriginList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *corsOriginList) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errEmptyCORSOrigin
	}
	*l = append(*l, trimmed)
	return nil
}

type corsOriginError struct{}

func (corsOriginError) Error() string { return "--cors-origin must not be empty" }

var errEmptyCORSOrigin = corsOriginError{}
