package s3api

import (
	"errors"
	"net/http"

	"github.com/chester-hill-solutions/stow-s3/internal/auth"
)

// AuthFunc validates an incoming request. Return nil to allow.
type AuthFunc func(r *http.Request) error

// DevBypass skips authentication (for tests).
func DevBypass(_ *http.Request) error {
	return nil
}

// SigV4Auth returns an AuthFunc backed by the given verifier and credentials.
func SigV4Auth(v *auth.Verifier, creds auth.Credentials) AuthFunc {
	return func(r *http.Request) error {
		return v.Authenticate(r, creds)
	}
}

func authError(err error) s3Error {
	var authErr *auth.Error
	if errors.As(err, &authErr) {
		status := http.StatusForbidden
		switch authErr.Code {
		case "RequestTimeTooSkewed":
			// already 403
		case "SignatureDoesNotMatch":
			// already 403
		default:
			if authErr.Code == "AccessDenied" {
				status = http.StatusForbidden
			}
		}
		return s3Error{Code: authErr.Code, Message: authErr.Message, StatusCode: status}
	}
	return s3Error{Code: "AccessDenied", Message: "Access Denied", StatusCode: http.StatusForbidden}
}
