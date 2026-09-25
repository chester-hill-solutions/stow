package storage

import "strings"

// CheckWritePreconditions applies the shared object-write preconditions.
func CheckWritePreconditions(opts PutOptions, existing *ObjectMeta) error {
	if opts.IfMatch != "" {
		if existing == nil || !matchesETagHeader(opts.IfMatch, existing.ETag, false) {
			return ErrPreconditionFailed
		}
	}
	if opts.IfNoneMatch != "" && existing != nil && matchesETagHeader(opts.IfNoneMatch, existing.ETag, true) {
		return ErrPreconditionFailed
	}
	return nil
}

func matchesETagHeader(header, actual string, weak bool) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidateValue := candidate
		if strings.HasPrefix(strings.ToLower(candidateValue), "w/") {
			if !weak {
				continue
			}
			candidateValue = candidateValue[2:]
		}
		if strings.EqualFold(strings.Trim(candidateValue, "\""), strings.Trim(actual, "\"")) {
			return true
		}
	}
	return false
}
