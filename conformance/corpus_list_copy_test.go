package conformance_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func runCorpusListObjects(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	pages := testCase.Expect.Pages
	if len(pages) == 0 {
		pages = []sharedCorpusPage{{
			Contents:       testCase.Expect.Contents,
			CommonPrefixes: testCase.Expect.CommonPrefixes,
			KeyCount:       testCase.Expect.KeyCount,
			IsTruncated:    testCase.Expect.IsTruncated != nil && *testCase.Expect.IsTruncated,
		}}
	}
	var continuationToken *string
	for index, expectedPage := range pages {
		input := &s3.ListObjectsV2Input{
			Bucket:            aws.String(corpusContext.bucket(testCase.Bucket)),
			Prefix:            optionalString(testCase.Prefix),
			Delimiter:         optionalString(testCase.Delimiter),
			MaxKeys:           aws.Int32(int32(testCase.MaxKeys)),
			ContinuationToken: continuationToken,
			EncodingType:      types.EncodingType(testCase.EncodingType),
		}
		output, err := corpusContext.env.Client.ListObjectsV2(corpusContext.ctx, input)
		if err != nil {
			corpusContext.t.Fatalf("ListObjectsV2 page %d: %v", index+1, err)
		}
		assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
		assertCorpusListPage(corpusContext.t, output, expectedPage, index)
		isTruncated := aws.ToBool(output.IsTruncated)
		if isTruncated != expectedPage.IsTruncated {
			corpusContext.t.Fatalf("page %d IsTruncated = %t, want %t", index+1, isTruncated, expectedPage.IsTruncated)
		}
		if index < len(pages)-1 {
			if !isTruncated || output.NextContinuationToken == nil || *output.NextContinuationToken == "" {
				corpusContext.t.Fatalf("page %d did not provide a continuation token", index+1)
			}
			continuationToken = output.NextContinuationToken
			continue
		}
		if isTruncated {
			corpusContext.t.Fatalf("final page %d is unexpectedly truncated", index+1)
		}
	}
}

func assertCorpusListPage(t *testing.T, output *s3.ListObjectsV2Output, expected sharedCorpusPage, index int) {
	t.Helper()
	contents := make([]string, 0, len(output.Contents))
	for _, object := range output.Contents {
		contents = append(contents, aws.ToString(object.Key))
	}
	prefixes := make([]string, 0, len(output.CommonPrefixes))
	for _, commonPrefix := range output.CommonPrefixes {
		prefixes = append(prefixes, aws.ToString(commonPrefix.Prefix))
	}
	if !slices.Equal(contents, expected.Contents) {
		t.Fatalf("page %d contents = %q, want %q", index+1, contents, expected.Contents)
	}
	if !slices.Equal(prefixes, expected.CommonPrefixes) {
		t.Fatalf("page %d common prefixes = %q, want %q", index+1, prefixes, expected.CommonPrefixes)
	}
	if got := aws.ToInt32(output.KeyCount); got != int32(expected.KeyCount) {
		t.Fatalf("page %d KeyCount = %d, want %d", index+1, got, expected.KeyCount)
	}
}

func runCorpusCopyObject(corpusContext *sharedCorpusContext) {
	corpusContext.t.Helper()
	testCase := corpusContext.testCase
	destinationBucket := testCase.destinationBucket()
	input := &s3.CopyObjectInput{
		Bucket:                aws.String(corpusContext.bucket(destinationBucket)),
		Key:                   aws.String(testCase.DestinationKey),
		CopySource:            aws.String(corpusContext.bucket(testCase.SourceBucket) + "/" + testCase.SourceKey),
		CopySourceIfMatch:     optionalString(testCase.CopySourceIfMatch),
		CopySourceIfNoneMatch: optionalString(testCase.CopySourceIfNoneMatch),
	}
	if testCase.MetadataDirective != "" {
		input.MetadataDirective = types.MetadataDirective(strings.ToUpper(testCase.MetadataDirective))
		input.ContentType = optionalString(testCase.ContentType)
		input.Metadata = testCase.Metadata
	}
	output, err := corpusContext.env.Client.CopyObject(corpusContext.ctx, input)
	if testCase.Expect.Status >= http.StatusBadRequest {
		assertCorpusError(corpusContext.t, err, testCase.Expect)
		return
	}
	if err != nil {
		corpusContext.t.Fatalf("CopyObject: %v", err)
	}
	assertCorpusStatus(corpusContext.t, corpusContext.env, testCase.Expect.Status)
	if output.CopyObjectResult == nil {
		corpusContext.t.Fatal("CopyObject did not return CopyObjectResult")
	}
	assertCorpusETag(corpusContext.t, aws.ToString(output.CopyObjectResult.ETag))
	assertCorpusGet(corpusContext, destinationBucket, testCase.DestinationKey, testCase.Expect)
}
