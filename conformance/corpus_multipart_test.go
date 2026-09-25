package conformance_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func runCorpusMultipart(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	bucket := corpusContext.bucket(testCase.Bucket)
	created, err := corpusContext.env.Client.CreateMultipartUpload(corpusContext.ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(testCase.Key),
	})
	if err != nil {
		corpusContext.t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := aws.ToString(created.UploadId)
	if uploadID == "" {
		corpusContext.t.Fatal("CreateMultipartUpload returned an empty upload ID")
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
	completed := false
	defer func() {
		if completed {
			return
		}
		_, _ = corpusContext.env.Client.AbortMultipartUpload(corpusContext.ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String(testCase.Key),
			UploadId: aws.String(uploadID),
		})
	}()

	if testCase.ListMultipartUploads {
		assertCorpusMultipartListing(corpusContext, bucket, testCase.Key, uploadID)
	}
	completedParts := make([]types.CompletedPart, 0, len(testCase.Parts))
	var expectedBody []byte
	for _, part := range testCase.Parts {
		body := corpusPartBody(part)
		uploaded, uploadErr := corpusContext.env.Client.UploadPart(corpusContext.ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(testCase.Key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(int32(part.Number)),
			Body:       bytes.NewReader(body),
		})
		if uploadErr != nil {
			corpusContext.t.Fatalf("UploadPart(%d): %v", part.Number, uploadErr)
		}
		assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
		assertCorpusETag(corpusContext.t, aws.ToString(uploaded.ETag))
		completedParts = append(completedParts, types.CompletedPart{
			ETag: uploaded.ETag, PartNumber: aws.Int32(int32(part.Number)),
		})
		expectedBody = append(expectedBody, body...)
	}
	assertCorpusPartsListing(corpusContext, testCase, uploadID)
	finished, err := corpusContext.env.Client.CompleteMultipartUpload(corpusContext.ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(testCase.Key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completedParts},
	})
	if err != nil {
		corpusContext.t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	completed = true
	assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
	assertCorpusMultipartETag(corpusContext.t, aws.ToString(finished.ETag), testCase.Expect.ETag)
	assertCorpusMultipartBody(corpusContext, bucket, testCase.Key, expectedBody, testCase.Expect.BodyLength)
}

func assertCorpusMultipartListing(corpusContext *sharedCorpusContext, bucket, key, uploadID string) {
	corpusContext.t.Helper()
	output, err := corpusContext.env.Client.ListMultipartUploads(corpusContext.ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		corpusContext.t.Fatalf("ListMultipartUploads: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
	if len(output.Uploads) != 1 {
		corpusContext.t.Fatalf("active multipart count = %d, want 1", len(output.Uploads))
	}
	if got := aws.ToString(output.Uploads[0].Key); got != key {
		corpusContext.t.Fatalf("active multipart key = %q, want %q", got, key)
	}
	if got := aws.ToString(output.Uploads[0].UploadId); got != uploadID {
		corpusContext.t.Fatalf("active multipart upload ID = %q, want %q", got, uploadID)
	}
}

func assertCorpusPartsListing(corpusContext *sharedCorpusContext, testCase sharedCorpusCase, uploadID string) {
	corpusContext.t.Helper()
	output, err := corpusContext.env.Client.ListParts(corpusContext.ctx, &s3.ListPartsInput{
		Bucket:   aws.String(corpusContext.bucket(testCase.Bucket)),
		Key:      aws.String(testCase.Key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		corpusContext.t.Fatalf("ListParts: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, http.StatusOK)
	actual := make([]int, 0, len(output.Parts))
	for _, part := range output.Parts {
		actual = append(actual, int(aws.ToInt32(part.PartNumber)))
	}
	expected := testCase.Expect.PartNumbers
	if len(expected) == 0 {
		expected = make([]int, 0, len(testCase.Parts))
		for _, part := range testCase.Parts {
			expected = append(expected, part.Number)
		}
	}
	if len(actual) != len(expected) {
		corpusContext.t.Fatalf("ListParts numbers = %v, want %v", actual, expected)
	}
	for index := range actual {
		if actual[index] != expected[index] {
			corpusContext.t.Fatalf("ListParts number %d = %d, want %d", index, actual[index], expected[index])
		}
	}
}

func assertCorpusMultipartETag(t *testing.T, got, expected string) {
	t.Helper()
	if expected == "" {
		assertCorpusETag(t, got)
		return
	}
	if got != expected {
		t.Fatalf("multipart ETag = %q, want %q", got, expected)
	}
}

func assertCorpusMultipartBody(corpusContext *sharedCorpusContext, bucket, key string, expected []byte, expectedLength int64) {
	corpusContext.t.Helper()
	output, err := corpusContext.env.Client.GetObject(corpusContext.ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		corpusContext.t.Fatalf("GetObject multipart result: %v", err)
	}
	defer output.Body.Close()
	body, err := io.ReadAll(output.Body)
	if err != nil {
		corpusContext.t.Fatalf("read multipart result: %v", err)
	}
	if !bytes.Equal(body, expected) {
		corpusContext.t.Fatalf("multipart body differs from concatenated parts")
	}
	if expectedLength > 0 && int64(len(body)) != expectedLength {
		corpusContext.t.Fatalf("multipart body length = %d, want %d", len(body), expectedLength)
	}
}
