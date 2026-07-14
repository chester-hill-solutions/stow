package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithmAWS4HMACSHA256 = "AWS4-HMAC-SHA256"
	serviceS3               = "s3"
	unsignedPayload         = "UNSIGNED-PAYLOAD"
	emptyPayloadHash        = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	amzDateLayout           = "20060102T150405Z"
	dateStampLayout         = "20060102"
)

type signedRequest struct {
	algorithm     string
	credential    credentialScope
	signedHeaders []string
	signature     string
	amzDate       string
	payloadHash   string
	presigned     bool
	expires       int
}

type credentialScope struct {
	accessKeyID string
	dateStamp   string
	region      string
	service     string
}

func parseAuthorizationHeader(value string) (signedRequest, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return signedRequest{}, authError("AccessDenied", "missing Authorization header")
	}

	parts := strings.SplitN(value, " ", 2)
	if len(parts) != 2 || parts[0] != algorithmAWS4HMACSHA256 {
		return signedRequest{}, authError("AccessDenied", "malformed Authorization header")
	}

	var sr signedRequest
	sr.algorithm = parts[0]

	for _, segment := range strings.Split(parts[1], ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		kv := strings.SplitN(segment, "=", 2)
		if len(kv) != 2 {
			return signedRequest{}, authError("AccessDenied", "malformed Authorization header")
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "Credential":
			scope, err := parseCredentialScope(val)
			if err != nil {
				return signedRequest{}, err
			}
			sr.credential = scope
		case "SignedHeaders":
			sr.signedHeaders = parseSignedHeaders(val)
		case "Signature":
			sr.signature = val
		}
	}

	if sr.credential.accessKeyID == "" || len(sr.signedHeaders) == 0 || sr.signature == "" {
		return signedRequest{}, authError("AccessDenied", "malformed Authorization header")
	}
	return sr, nil
}

func parsePresignedQuery(query url.Values) (signedRequest, error) {
	algorithm := queryValue(query, "X-Amz-Algorithm")
	if algorithm == "" {
		return signedRequest{}, authError("AccessDenied", "missing presigned auth parameters")
	}
	if algorithm != algorithmAWS4HMACSHA256 {
		return signedRequest{}, authError("AccessDenied", "unsupported presigned algorithm")
	}

	credentialRaw := queryValue(query, "X-Amz-Credential")
	scope, err := parseCredentialScope(credentialRaw)
	if err != nil {
		return signedRequest{}, err
	}

	amzDate := queryValue(query, "X-Amz-Date")
	if amzDate == "" {
		return signedRequest{}, authError("AccessDenied", "missing X-Amz-Date")
	}

	expiresRaw := queryValue(query, "X-Amz-Expires")
	expires, err := strconv.Atoi(expiresRaw)
	if err != nil || expires <= 0 {
		return signedRequest{}, authError("AccessDenied", "invalid X-Amz-Expires")
	}

	signedHeaders := parseSignedHeaders(queryValue(query, "X-Amz-SignedHeaders"))
	if len(signedHeaders) == 0 {
		return signedRequest{}, authError("AccessDenied", "missing X-Amz-SignedHeaders")
	}

	signature := queryValue(query, "X-Amz-Signature")
	if signature == "" {
		return signedRequest{}, authError("AccessDenied", "missing X-Amz-Signature")
	}

	return signedRequest{
		algorithm:     algorithm,
		credential:    scope,
		signedHeaders: signedHeaders,
		signature:     signature,
		amzDate:       amzDate,
		payloadHash:   unsignedPayload,
		presigned:     true,
		expires:       expires,
	}, nil
}

func parseCredentialScope(raw string) (credentialScope, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" {
		return credentialScope{}, authError("AccessDenied", "malformed credential scope")
	}
	return credentialScope{
		accessKeyID: parts[0],
		dateStamp:   parts[1],
		region:      parts[2],
		service:     parts[3],
	}, nil
}

func parseSignedHeaders(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func queryValue(query url.Values, key string) string {
	for k, values := range query {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func detectSignedRequest(r *http.Request) (signedRequest, error) {
	query := r.URL.Query()
	if queryValue(query, "X-Amz-Algorithm") != "" || queryValue(query, "X-Amz-Signature") != "" {
		return parsePresignedQuery(query)
	}

	authHeader := r.Header.Get("Authorization")
	sr, err := parseAuthorizationHeader(authHeader)
	if err != nil {
		return signedRequest{}, err
	}

	sr.amzDate = headerValue(r.Header, "X-Amz-Date")
	if sr.amzDate == "" {
		sr.amzDate = headerValue(r.Header, "Date")
	}
	if sr.amzDate == "" {
		return signedRequest{}, authError("AccessDenied", "missing x-amz-date")
	}

	sr.payloadHash = headerValue(r.Header, "X-Amz-Content-Sha256")
	if sr.payloadHash == "" {
		sr.payloadHash = emptyPayloadHash
	}
	return sr, nil
}

func headerValue(headers http.Header, name string) string {
	for k, values := range headers {
		if strings.EqualFold(k, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func verifySignedRequest(r *http.Request, creds Credentials, region string, maxSkew time.Duration, now time.Time) error {
	sr, err := detectSignedRequest(r)
	if err != nil {
		return err
	}

	if sr.credential.accessKeyID != creds.AccessKeyID {
		return authError("AccessDenied", "unknown access key")
	}
	if sr.credential.service != serviceS3 {
		return authError("AccessDenied", "unsupported service in credential scope")
	}
	if sr.credential.region != region {
		return authError("AccessDenied", "region mismatch")
	}
	if !strings.HasPrefix(sr.amzDate, sr.credential.dateStamp) {
		return authError("AccessDenied", "credential date mismatch")
	}

	requestTime, err := parseAmzTime(sr.amzDate)
	if err != nil {
		return err
	}

	if sr.presigned {
		expiry := requestTime.Add(time.Duration(sr.expires) * time.Second)
		if now.After(expiry) {
			return authError("AccessDenied", "request has expired")
		}
	} else if skew := now.Sub(requestTime); skew > maxSkew || skew < -maxSkew {
		return authError("RequestTimeTooSkewed", "request time skew too large")
	}

	if err := verifyPayloadHash(r, sr); err != nil {
		return err
	}

	expected, err := computeSignature(r, sr, creds.SecretAccessKey)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(strings.ToLower(sr.signature)), []byte(strings.ToLower(expected))) {
		return authError("SignatureDoesNotMatch", "signature mismatch")
	}
	return nil
}

func verifyPayloadHash(r *http.Request, sr signedRequest) error {
	if sr.presigned {
		return nil
	}
	if strings.EqualFold(sr.payloadHash, unsignedPayload) {
		return nil
	}
	if len(sr.payloadHash) != 64 {
		return authError("AccessDenied", "invalid x-amz-content-sha256")
	}
	bodyHash, err := hashRequestBody(r)
	if err != nil {
		return err
	}
	if !strings.EqualFold(bodyHash, sr.payloadHash) {
		return authError("SignatureDoesNotMatch", "payload hash mismatch")
	}
	return nil
}

func hashRequestBody(r *http.Request) (string, error) {
	if r.Body == nil || r.ContentLength == 0 {
		return emptyPayloadHash, nil
	}
	// Body hashing is only required when the client signed the payload hash.
	// Callers that need streaming verification should provide GetBody.
	if r.GetBody == nil {
		return "", authError("AccessDenied", "cannot verify signed payload without request body")
	}
	rc, err := r.GetBody()
	if err != nil {
		return "", authError("AccessDenied", "cannot read request body")
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := copyLimited(h, rc); err != nil {
		return "", authError("AccessDenied", "cannot read request body")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseAmzTime(raw string) (time.Time, error) {
	if t, err := time.Parse(amzDateLayout, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC1123, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, authError("AccessDenied", "malformed request date")
}

func computeSignature(r *http.Request, sr signedRequest, secret string) (string, error) {
	canonicalRequest, err := buildCanonicalRequest(r, sr)
	if err != nil {
		return "", err
	}
	stringToSign := buildStringToSign(sr.amzDate, sr.credential, canonicalRequest)
	signingKey := deriveSigningKey(secret, sr.credential.dateStamp, sr.credential.region, sr.credential.service)
	return hex.EncodeToString(hmacSHA256(signingKey, stringToSign)), nil
}

func buildCanonicalRequest(r *http.Request, sr signedRequest) (string, error) {
	canonicalURI := canonicalURIPath(r.URL.EscapedPath())
	canonicalQuery := canonicalQueryString(r.URL.RawQuery)
	canonicalHeaders, signedHeaders, err := canonicalHeaders(r, sr.signedHeaders)
	if err != nil {
		return "", err
	}
	if signedHeaders != strings.Join(sr.signedHeaders, ";") {
		return "", authError("AccessDenied", "signed headers mismatch")
	}
	return strings.Join([]string{
		r.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		sr.payloadHash,
	}, "\n"), nil
}

func buildStringToSign(amzDate string, scope credentialScope, canonicalRequest string) string {
	scopeString := fmt.Sprintf("%s/%s/%s/aws4_request", scope.dateStamp, scope.region, scope.service)
	hash := sha256.Sum256([]byte(canonicalRequest))
	return strings.Join([]string{
		algorithmAWS4HMACSHA256,
		amzDate,
		scopeString,
		hex.EncodeToString(hash[:]),
	}, "\n")
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

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
