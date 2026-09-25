package conformance_test

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type sharedCorpus struct {
	Version int                `json:"version"`
	Cases   []sharedCorpusCase `json:"cases"`
}

type sharedCorpusCase struct {
	ID          string                  `json:"id"`
	Operation   string                  `json:"operation"`
	Bucket      string                  `json:"bucket"`
	Key         string                  `json:"key"`
	Body        string                  `json:"body"`
	ContentType string                  `json:"contentType"`
	Metadata    map[string]string       `json:"metadata"`
	Setup       []sharedCorpusObject    `json:"setup"`
	Expect      sharedCorpusExpectation `json:"expect"`

	IfNoneMatch string `json:"ifNoneMatch"`
	IfMatch     string `json:"ifMatch"`

	ContentMD5        string `json:"contentMD5"`
	ChecksumAlgorithm string `json:"checksumAlgorithm"`
	ChecksumValue     string `json:"checksumValue"`

	Prefix       string `json:"prefix"`
	Delimiter    string `json:"delimiter"`
	MaxKeys      int    `json:"maxKeys"`
	EncodingType string `json:"encodingType"`

	SourceBucket          string `json:"sourceBucket"`
	SourceKey             string `json:"sourceKey"`
	DestinationBucket     string `json:"destinationBucket"`
	DestinationKey        string `json:"destinationKey"`
	MetadataDirective     string `json:"metadataDirective"`
	CopySourceIfMatch     string `json:"copySourceIfMatch"`
	CopySourceIfNoneMatch string `json:"copySourceIfNoneMatch"`

	Parts                []sharedCorpusPart `json:"parts"`
	ListMultipartUploads bool               `json:"listMultipartUploads"`
}

type sharedCorpusObject struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Body        string            `json:"body"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
}

type sharedCorpusPart struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
	Repeat int    `json:"repeat"`
}

type sharedCorpusExpectation struct {
	Status            int                `json:"status"`
	Body              string             `json:"body"`
	ContentType       string             `json:"contentType"`
	Metadata          map[string]string  `json:"metadata"`
	ErrorCode         string             `json:"errorCode"`
	ETag              string             `json:"etag"`
	BodyLength        int64              `json:"bodyLength"`
	ChecksumAlgorithm string             `json:"checksumAlgorithm"`
	ChecksumValue     string             `json:"checksumValue"`
	Contents          []string           `json:"contents"`
	CommonPrefixes    []string           `json:"commonPrefixes"`
	KeyCount          int                `json:"keyCount"`
	IsTruncated       *bool              `json:"isTruncated"`
	Pages             []sharedCorpusPage `json:"pages"`
	PartNumbers       []int              `json:"partNumbers"`
}

type sharedCorpusPage struct {
	Contents       []string `json:"contents"`
	CommonPrefixes []string `json:"commonPrefixes"`
	KeyCount       int      `json:"keyCount"`
	IsTruncated    bool     `json:"isTruncated"`
}

type sharedCorpusContext struct {
	t        *testing.T
	testCase sharedCorpusCase
	env      *testEnv
	ctx      context.Context
	buckets  map[string]string
}

func newSharedCorpusContext(t *testing.T, testCase sharedCorpusCase) *sharedCorpusContext {
	t.Helper()
	corpusContext := &sharedCorpusContext{
		t:        t,
		testCase: testCase,
		env:      newTestEnv(t),
		ctx:      context.Background(),
		buckets:  make(map[string]string),
	}
	for _, logicalBucket := range testCase.logicalBuckets() {
		corpusContext.buckets[logicalBucket] = uniqueBucket(t, "corpus", testCase.ID, logicalBucket)
	}
	for _, logicalBucket := range sortedMapKeys(corpusContext.buckets) {
		createBucket(corpusContext.ctx, t, corpusContext.env.Client, corpusContext.buckets[logicalBucket])
	}
	return corpusContext
}

func (c sharedCorpusCase) logicalBuckets() []string {
	seen := make(map[string]struct{})
	add := func(bucket string) {
		if bucket != "" {
			seen[bucket] = struct{}{}
		}
	}
	add(c.Bucket)
	add(c.SourceBucket)
	add(c.DestinationBucket)
	for _, object := range c.Setup {
		add(object.Bucket)
	}
	buckets := make([]string, 0, len(seen))
	for bucket := range seen {
		buckets = append(buckets, bucket)
	}
	sort.Strings(buckets)
	return buckets
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c *sharedCorpusContext) bucket(logical string) string {
	if logical == "" {
		logical = c.testCase.Bucket
	}
	mapped, ok := c.buckets[logical]
	if !ok {
		c.t.Fatalf("corpus case %q references unknown bucket %q", c.testCase.ID, logical)
	}
	return mapped
}

func (c *sharedCorpusContext) objectBucket(object sharedCorpusObject) string {
	return c.bucket(object.Bucket)
}

func corpusBody(body string, repeat int) []byte {
	if repeat <= 1 {
		return []byte(body)
	}
	return []byte(strings.Repeat(body, repeat))
}

func corpusPartBody(part sharedCorpusPart) []byte {
	return corpusBody(part.Body, part.Repeat)
}

func (c sharedCorpusCase) destinationBucket() string {
	if c.DestinationBucket != "" {
		return c.DestinationBucket
	}
	return c.Bucket
}

func (c *sharedCorpusContext) putSetupObject(object sharedCorpusObject) error {
	c.t.Helper()
	input := &s3.PutObjectInput{
		Bucket:   aws.String(c.objectBucket(object)),
		Key:      aws.String(object.Key),
		Body:     bytes.NewReader([]byte(object.Body)),
		Metadata: object.Metadata,
	}
	if object.ContentType != "" {
		input.ContentType = aws.String(object.ContentType)
	}
	_, err := c.env.Client.PutObject(c.ctx, input)
	return err
}
