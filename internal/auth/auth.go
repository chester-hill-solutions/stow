// Package auth implements AWS Signature Version 4 verification.
package auth

import (
	"io"
	"net/http"
	"time"
)

const (
	// DefaultRegion is the configured S3 signing region for local stow.
	DefaultRegion = "us-east-1"
	// DefaultMaxSkew is the maximum allowed request clock skew.
	DefaultMaxSkew = 15 * time.Minute
)

// Verifier validates SigV4 header auth and presigned query-string auth.
type Verifier struct {
	Region  string
	MaxSkew time.Duration
}

// NewVerifier returns a verifier with defaults applied.
func NewVerifier(region string) *Verifier {
	if region == "" {
		region = DefaultRegion
	}
	return &Verifier{
		Region:  region,
		MaxSkew: DefaultMaxSkew,
	}
}

// Authenticate validates the request against the provided local credentials.
func Authenticate(r *http.Request, creds Credentials) error {
	return NewVerifier(DefaultRegion).Authenticate(r, creds)
}

// Authenticate validates the request against the provided local credentials.
func (v *Verifier) Authenticate(r *http.Request, creds Credentials) error {
	return v.AuthenticateAt(r, creds, time.Now().UTC())
}

// AuthenticateAt validates the request at the provided reference time.
func (v *Verifier) AuthenticateAt(r *http.Request, creds Credentials, now time.Time) error {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return authError("AccessDenied", "server credentials are not configured")
	}
	region := v.Region
	if region == "" {
		region = DefaultRegion
	}
	maxSkew := v.MaxSkew
	if maxSkew == 0 {
		maxSkew = DefaultMaxSkew
	}
	return verifySignedRequest(r, creds, region, maxSkew, now.UTC())
}

func copyLimited(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}
