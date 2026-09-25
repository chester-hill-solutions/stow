package runthrough

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

// RetryClass describes whether an upstream failure is safe to retry.
//
// Unknown errors are conservatively treated as transient by classifyRetry. An
// upstream operation must never be silently discarded merely because a
// provider returned an error shape that this package does not recognize.
type RetryClass uint8

const (
	RetryClassUnknown RetryClass = iota
	RetryClassTransient
	RetryClassDeterministic
)

// Short aliases keep the classification convenient for callers while the
// explicit names remain useful in persisted/inspection code.
const (
	RetryTransient     = RetryClassTransient
	RetryDeterministic = RetryClassDeterministic
)

func (c RetryClass) String() string {
	switch c {
	case RetryClassTransient:
		return "transient"
	case RetryClassDeterministic:
		return "deterministic"
	default:
		return "unknown"
	}
}

// UpstreamError preserves the provider response metadata needed by the
// outbox while retaining the original SDK error for errors.Is/errors.As.
type UpstreamError struct {
	Err        error
	StatusCode int
	Code       string
	Class      RetryClass
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code != "" {
		return fmt.Sprintf("upstream error %s (status %d)", e.Code, e.StatusCode)
	}
	return fmt.Sprintf("upstream error (status %d)", e.StatusCode)
}

func (e *UpstreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// HTTPStatus exposes the response status without requiring callers to know
// which concrete SDK error type carried it.
func (e *UpstreamError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// Status exposes the response status using the short accessor shape used by
// several S3-compatible client implementations.
func (e *UpstreamError) Status() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// ErrorCode exposes the provider error code when one is available.
func (e *UpstreamError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// RetryClass exposes the classification carried by a typed provider error.
func (e *UpstreamError) RetryClass() RetryClass {
	if e == nil {
		return RetryClassUnknown
	}
	if e.Class != RetryClassUnknown {
		return e.Class
	}
	if class := classifyHTTPStatus(e.StatusCode); class != RetryClassUnknown {
		return class
	}
	return classifyErrorCode(e.Code)
}

// NewUpstreamError builds a typed provider error. A 4xx response is
// deterministic except for timeout/throttling responses, which are safe to
// retry; 5xx responses are transient.
func NewUpstreamError(statusCode int, cause error) *UpstreamError {
	return &UpstreamError{
		Err:        cause,
		StatusCode: statusCode,
		Code:       upstreamErrorCode(cause),
		Class:      classifyHTTPStatus(statusCode),
	}
}

// NewDeterministicUpstreamError marks a provider failure as terminal when no
// HTTP status is available.
func NewDeterministicUpstreamError(cause error) *UpstreamError {
	return &UpstreamError{Err: cause, Class: RetryClassDeterministic}
}

// NewTransientUpstreamError marks a provider failure as retryable when no HTTP
// status is available.
func NewTransientUpstreamError(cause error) *UpstreamError {
	return &UpstreamError{Err: cause, Class: RetryClassTransient}
}

// ClassifyRetry returns the durable-outbox retry disposition for err.
func ClassifyRetry(err error) RetryClass {
	return classifyRetry(err)
}

// IsTransientRetry reports whether err should receive a future retry time.
func IsTransientRetry(err error) bool {
	return classifyRetry(err) == RetryClassTransient
}

// IsDeterministicRetry reports whether err must be retained as terminal.
func IsDeterministicRetry(err error) bool {
	return classifyRetry(err) == RetryClassDeterministic
}

func classifyRetry(err error) RetryClass {
	if err == nil {
		return RetryClassUnknown
	}
	if errors.Is(err, ErrOutboxVersionConflict) {
		return RetryClassDeterministic
	}
	if class, ok := explicitRetryClass(err); ok {
		return class
	}
	if class := typedUpstreamClass(err); class != RetryClassUnknown {
		return class
	}
	if status := upstreamHTTPStatus(err); status != 0 {
		return classifyHTTPStatus(status)
	}
	if class := classifyErrorCode(upstreamErrorCode(err)); class != RetryClassUnknown {
		return class
	}
	if isDeterministicStorageError(err) {
		return RetryClassDeterministic
	}
	if isTransientContextError(err) || isTransientNetworkError(err) {
		return RetryClassTransient
	}
	if class := classifyAPIError(err); class != RetryClassUnknown {
		return class
	}

	// Unknown provider/network errors are retried rather than dropped. The
	// durable outbox is the safety boundary for an unclassified failure.
	return RetryClassTransient
}

func explicitRetryClass(err error) (RetryClass, bool) {
	var retryClassifier interface{ RetryClass() RetryClass }
	if errors.As(err, &retryClassifier) {
		if class := retryClassifier.RetryClass(); class != RetryClassUnknown {
			return class, true
		}
	}
	var retryable interface{ Retryable() bool }
	if errors.As(err, &retryable) {
		if retryable.Retryable() {
			return RetryClassTransient, true
		}
		return RetryClassDeterministic, true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return RetryClassTransient, true
	}
	return RetryClassUnknown, false
}

func typedUpstreamClass(err error) RetryClass {
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr == nil {
		return RetryClassUnknown
	}
	if upstreamErr.Class != RetryClassUnknown {
		return upstreamErr.Class
	}
	if class := classifyHTTPStatus(upstreamErr.StatusCode); class != RetryClassUnknown {
		return class
	}
	return classifyErrorCode(upstreamErr.Code)
}

func isDeterministicStorageError(err error) bool {
	known := []error{
		storage.ErrObjectNotFound,
		storage.ErrBucketNotFound,
		storage.ErrInvalidBucketName,
		storage.ErrBucketExists,
		storage.ErrInvalidKey,
		storage.ErrPreconditionFailed,
		storage.ErrChecksumMismatch,
		storage.ErrMD5Mismatch,
		storage.ErrBucketNotEmpty,
		storage.ErrInvalidPart,
		storage.ErrInvalidUpload,
		storage.ErrUploadNotFound,
		storage.ErrNoSuchUpload,
	}
	for _, target := range known {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func isTransientContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isTransientNetworkError(err error) bool {
	var connectionErr interface{ ConnectionError() bool }
	if errors.As(err, &connectionErr) && connectionErr.ConnectionError() {
		return true
	}
	var canceledErr interface{ CanceledError() bool }
	if errors.As(err, &canceledErr) && canceledErr.CanceledError() {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func classifyAPIError(err error) RetryClass {
	var apiFault interface {
		ErrorFault() smithy.ErrorFault
	}
	if !errors.As(err, &apiFault) {
		return RetryClassUnknown
	}
	switch apiFault.ErrorFault() {
	case smithy.FaultClient:
		return RetryClassDeterministic
	case smithy.FaultServer:
		return RetryClassTransient
	default:
		return RetryClassUnknown
	}
}

func classifyHTTPStatus(status int) RetryClass {
	switch {
	case status == http.StatusRequestTimeout,
		status == http.StatusTooEarly,
		status == http.StatusTooManyRequests:
		return RetryClassTransient
	case status >= 400 && status < 500:
		return RetryClassDeterministic
	case status >= 500 && status < 600:
		return RetryClassTransient
	default:
		return RetryClassUnknown
	}
}

func classifyErrorCode(code string) RetryClass {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "expiredtoken", "expiredtokenexception", "invalidtoken", "unrecognizedclientexception", "invalidrequest", "invalidargument", "invalidbucketname", "invalidpart", "invalidobjectstate", "nosuchbucket", "nosuchkey", "notfound", "preconditionfailed", "conditionalrequestconflict":
		return RetryClassDeterministic
	case "slowdown", "requesttimeout", "requesttimeoutexception", "throttling", "throttlingexception", "requestlimitexceeded", "provisionedthroughputexceededexception", "serviceunavailable", "internalservererror", "internalerror", "operationaborted", "too manyrequests", "toomanyrequests", "timeout", "networkingerror", "connectionerror":
		return RetryClassTransient
	default:
		return RetryClassUnknown
	}
}

func upstreamHTTPStatus(err error) int {
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil && responseErr.Response != nil {
		return responseErr.HTTPStatusCode()
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(err, &statusErr) {
		return statusErr.HTTPStatusCode()
	}
	var statusCodeErr interface{ StatusCode() int }
	if errors.As(err, &statusCodeErr) {
		return statusCodeErr.StatusCode()
	}
	var httpStatusErr interface{ HTTPStatus() int }
	if errors.As(err, &httpStatusErr) {
		return httpStatusErr.HTTPStatus()
	}
	var genericStatusErr interface{ Status() int }
	if errors.As(err, &genericStatusErr) {
		return genericStatusErr.Status()
	}
	return 0
}

func upstreamErrorCode(err error) string {
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}
