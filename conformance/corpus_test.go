package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type sharedCorpus struct {
	Cases []sharedCorpusCase `json:"cases"`
}

type sharedCorpusCase struct {
	ID          string            `json:"id"`
	Operation   string            `json:"operation"`
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Body        string            `json:"body"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
	Expect      struct {
		Body        string            `json:"body"`
		ContentType string            `json:"contentType"`
		Metadata    map[string]string `json:"metadata"`
	} `json:"expect"`
}

func TestSharedCorpusRoundTrip(t *testing.T) {
	data, err := os.ReadFile("corpus/cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus sharedCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("shared corpus is empty")
	}

	for _, testCase := range corpus.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			if testCase.Operation != "putGetRoundTrip" {
				t.Fatalf("unsupported corpus operation %q", testCase.Operation)
			}
			env := newTestEnv(t)
			ctx := context.Background()
			bucket := uniqueBucket(t, "corpus", testCase.ID)
			createBucket(ctx, t, env.Client, bucket)
			put, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
				Bucket:      aws.String(bucket),
				Key:         aws.String(testCase.Key),
				Body:        bytes.NewReader([]byte(testCase.Body)),
				ContentType: aws.String(testCase.ContentType),
				Metadata:    testCase.Metadata,
			})
			if err != nil {
				t.Fatalf("PutObject: %v", err)
			}
			get, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(testCase.Key),
			})
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			defer get.Body.Close()
			body, err := io.ReadAll(get.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(body) != testCase.Expect.Body {
				t.Fatalf("body = %q, want %q", body, testCase.Expect.Body)
			}
			if aws.ToString(get.ContentType) != testCase.Expect.ContentType {
				t.Fatalf("content type = %q, want %q", aws.ToString(get.ContentType), testCase.Expect.ContentType)
			}
			for key, value := range testCase.Expect.Metadata {
				if get.Metadata[key] != value {
					t.Fatalf("metadata %q = %q, want %q", key, get.Metadata[key], value)
				}
			}
			if aws.ToString(put.ETag) == "" {
				t.Fatal("PutObject did not return an ETag")
			}
		})
	}
}
