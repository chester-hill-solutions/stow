package conformance_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func runSharedCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	corpusContext := newSharedCorpusContext(t, testCase)
	seedCorpusSetup(t, corpusContext)
	switch testCase.Operation {
	case "putGetRoundTrip":
		runCorpusPutGetRoundTrip(corpusContext)
	case "conditionalPut":
		runCorpusPutWithOptions(corpusContext)
	case "conditionalGet":
		runCorpusConditionalGet(corpusContext)
	case "checksumPut":
		runCorpusPutWithOptions(corpusContext)
	case "listObjectsV2":
		runCorpusListObjects(corpusContext)
	case "copyObject":
		runCorpusCopyObject(corpusContext)
	case "multipartUpload":
		runCorpusMultipart(corpusContext)
	default:
		t.Fatalf("unsupported corpus operation %q", testCase.Operation)
	}
}

func seedCorpusSetup(t *testing.T, corpusContext *sharedCorpusContext) {
	t.Helper()
	for _, object := range corpusContext.testCase.Setup {
		err := corpusContext.putSetupObject(object)
		if err != nil {
			t.Fatalf("setup PutObject(%q): %v", object.Key, err)
		}
		assertCorpusStatus(t, corpusContext.env, http.StatusOK)
	}
}

func runCorpusPutGetRoundTrip(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	input := corpusPutInput(corpusContext, []byte(testCase.Body))
	output, err := corpusContext.env.Client.PutObject(corpusContext.ctx, input)
	if err != nil {
		corpusContext.t.Fatalf("PutObject: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
	assertCorpusETag(corpusContext.t, aws.ToString(output.ETag))
	head, err := corpusContext.env.Client.HeadObject(corpusContext.ctx, &s3.HeadObjectInput{
		Bucket: aws.String(corpusContext.bucket(testCase.Bucket)),
		Key:    aws.String(testCase.Key),
	})
	if err != nil {
		corpusContext.t.Fatalf("HeadObject: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
	if aws.ToString(head.ETag) != aws.ToString(output.ETag) {
		corpusContext.t.Fatalf("Head ETag %q differs from Put ETag %q", aws.ToString(head.ETag), aws.ToString(output.ETag))
	}
	if testCase.Expect.ContentType != "" && aws.ToString(head.ContentType) != testCase.Expect.ContentType {
		corpusContext.t.Fatalf("head content type = %q, want %q", aws.ToString(head.ContentType), testCase.Expect.ContentType)
	}
	assertCorpusGet(corpusContext, testCase.Bucket, testCase.Key, testCase.Expect)
}

func runCorpusPutWithOptions(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	input := corpusPutInput(corpusContext, []byte(testCase.Body))
	input.IfMatch = optionalString(testCase.IfMatch)
	input.IfNoneMatch = optionalString(testCase.IfNoneMatch)
	applyCorpusChecksum(input, testCase)
	output, err := corpusContext.env.Client.PutObject(corpusContext.ctx, input)
	if testCase.Expect.Status >= http.StatusBadRequest {
		assertCorpusError(corpusContext.t, err, testCase.Expect)
		return
	}
	if err != nil {
		corpusContext.t.Fatalf("PutObject: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
	assertCorpusETag(corpusContext.t, aws.ToString(output.ETag))
	assertCorpusChecksum(corpusContext.t, output, testCase.Expect)
	assertCorpusGet(corpusContext, testCase.Bucket, testCase.Key, testCase.Expect)
}

func runCorpusConditionalGet(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	output, err := corpusContext.env.Client.GetObject(corpusContext.ctx, &s3.GetObjectInput{
		Bucket:      aws.String(corpusContext.bucket(testCase.Bucket)),
		Key:         aws.String(testCase.Key),
		IfMatch:     optionalString(testCase.IfMatch),
		IfNoneMatch: optionalString(testCase.IfNoneMatch),
	})
	if testCase.Expect.Status >= http.StatusBadRequest {
		assertCorpusError(corpusContext.t, err, testCase.Expect)
		return
	}
	if err != nil {
		corpusContext.t.Fatalf("GetObject conditional: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
	assertCorpusGetOutput(corpusContext, output, testCase.Expect)
}

func corpusPutInput(corpusContext *sharedCorpusContext, body []byte) *s3.PutObjectInput {
	testCase := corpusContext.testCase
	return &s3.PutObjectInput{
		Bucket:      aws.String(corpusContext.bucket(testCase.Bucket)),
		Key:         aws.String(testCase.Key),
		Body:        bytes.NewReader(body),
		ContentType: optionalString(testCase.ContentType),
		Metadata:    testCase.Metadata,
	}
}

func applyCorpusChecksum(input *s3.PutObjectInput, testCase sharedCorpusCase) {
	if testCase.ContentMD5 != "" {
		input.ContentMD5 = aws.String(testCase.ContentMD5)
	}
	if testCase.ChecksumAlgorithm == "" || testCase.ChecksumValue == "" {
		return
	}
	algorithm := strings.ToUpper(testCase.ChecksumAlgorithm)
	value := testCase.ChecksumValue
	input.ChecksumAlgorithm = types.ChecksumAlgorithm(algorithm)
	switch algorithm {
	case "CRC32":
		input.ChecksumCRC32 = aws.String(value)
	case "CRC32C":
		input.ChecksumCRC32C = aws.String(value)
	case "SHA1":
		input.ChecksumSHA1 = aws.String(value)
	case "SHA256":
		input.ChecksumSHA256 = aws.String(value)
	default:
		// The corpus validator limits this to algorithms implemented by the service.
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func assertCorpusETag(t *testing.T, etag string) {
	t.Helper()
	if strings.Trim(etag, "\"") == "" {
		t.Fatal("operation did not return a non-empty ETag")
	}
}

func assertCorpusStatus(t *testing.T, env *testEnv, want int) {
	t.Helper()
	if got := env.Status.StatusCode(); got != want {
		t.Fatalf("response status = %d, want %d", got, want)
	}
}

func assertCorpusGet(corpusContext *sharedCorpusContext, logicalBucket, key string, expected sharedCorpusExpectation) {
	corpusContext.t.Helper()
	output, err := corpusContext.env.Client.GetObject(corpusContext.ctx, &s3.GetObjectInput{
		Bucket: aws.String(corpusContext.bucket(logicalBucket)),
		Key:    aws.String(key),
	})
	if err != nil {
		corpusContext.t.Fatalf("GetObject(%q): %v", key, err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
	assertCorpusGetOutput(corpusContext, output, expected)
}

func assertCorpusGetOutput(corpusContext *sharedCorpusContext, output *s3.GetObjectOutput, expected sharedCorpusExpectation) {
	corpusContext.t.Helper()
	defer output.Body.Close()
	body, err := io.ReadAll(output.Body)
	if err != nil {
		corpusContext.t.Fatalf("read GetObject: %v", err)
	}
	if string(body) != expected.Body {
		corpusContext.t.Fatalf("body = %q, want %q", body, expected.Body)
	}
	if expected.ContentType != "" && aws.ToString(output.ContentType) != expected.ContentType {
		corpusContext.t.Fatalf("content type = %q, want %q", aws.ToString(output.ContentType), expected.ContentType)
	}
	for key, value := range expected.Metadata {
		if output.Metadata[key] != value {
			corpusContext.t.Fatalf("metadata %q = %q, want %q", key, output.Metadata[key], value)
		}
	}
}

func assertCorpusChecksum(t *testing.T, output *s3.PutObjectOutput, expected sharedCorpusExpectation) {
	t.Helper()
	if expected.ChecksumAlgorithm == "" {
		return
	}
	var got string
	switch strings.ToUpper(expected.ChecksumAlgorithm) {
	case "CRC32":
		got = aws.ToString(output.ChecksumCRC32)
	case "CRC32C":
		got = aws.ToString(output.ChecksumCRC32C)
	case "SHA1":
		got = aws.ToString(output.ChecksumSHA1)
	case "SHA256":
		got = aws.ToString(output.ChecksumSHA256)
	}
	if got != expected.ChecksumValue {
		t.Fatalf("checksum = %q, want %q", got, expected.ChecksumValue)
	}
}

func assertCorpusError(t *testing.T, err error, expected sharedCorpusExpectation) {
	t.Helper()
	if err == nil {
		t.Fatal("expected corpus operation to fail")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("expected SDK response error, got %v", err)
	}
	if responseErr.HTTPStatusCode() != expected.Status {
		t.Fatalf("error status = %d, want %d", responseErr.HTTPStatusCode(), expected.Status)
	}
	if expected.ErrorCode != "" {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected S3 API error %q, got %v", expected.ErrorCode, err)
		}
		if apiErr.ErrorCode() != expected.ErrorCode {
			t.Fatalf("error code = %q, want %q", apiErr.ErrorCode(), expected.ErrorCode)
		}
	}
}
