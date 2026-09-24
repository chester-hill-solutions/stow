package storage

import "strings"

func checkWritePreconditions(opts PutOptions, existing *ObjectMeta) error {
	if opts.IfMatch != "" {
		if existing == nil || !matchesETagHeader(opts.IfMatch, existing.ETag) {
			return ErrPreconditionFailed
		}
	}
	if opts.IfNoneMatch != "" && existing != nil && matchesETagHeader(opts.IfNoneMatch, existing.ETag) {
		return ErrPreconditionFailed
	}
	return nil
}

func matchesETagHeader(header, actual string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.Trim(candidate, "\"") == strings.Trim(actual, "\"") {
			return true
		}
	}
	return false
}
