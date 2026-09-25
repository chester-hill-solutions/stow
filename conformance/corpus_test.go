package conformance_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSharedCorpus(t *testing.T) {
	corpus := loadSharedCorpus(t)
	for _, testCase := range corpus.Cases {
		testCase := testCase
		t.Run(testCase.ID, func(t *testing.T) {
			runSharedCorpusCase(t, testCase)
		})
	}
}

func loadSharedCorpus(t *testing.T) sharedCorpus {
	t.Helper()
	data, err := os.ReadFile("corpus/cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var corpus sharedCorpus
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			t.Fatal("corpus contains multiple JSON values")
		}
		t.Fatalf("decode trailing corpus data: %v", err)
	}
	if corpus.Version != 1 {
		t.Fatalf("unsupported corpus version %d", corpus.Version)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("shared corpus is empty")
	}
	seen := make(map[string]struct{}, len(corpus.Cases))
	for _, testCase := range corpus.Cases {
		if testCase.ID == "" {
			t.Fatal("corpus case has an empty id")
		}
		if _, exists := seen[testCase.ID]; exists {
			t.Fatalf("duplicate corpus case id %q", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
		validateSharedCorpusCase(t, testCase)
	}
	return corpus
}

func supportedCorpusChecksum(algorithm string) bool {
	switch strings.ToUpper(algorithm) {
	case "CRC32", "CRC32C", "SHA1", "SHA256":
		return true
	default:
		return false
	}
}

func validateSharedCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	if testCase.Operation == "" {
		t.Fatalf("corpus case %q has no operation", testCase.ID)
	}
	if testCase.Expect.Status == 0 {
		t.Fatalf("corpus case %q has no expected status", testCase.ID)
	}
	validators := map[string]func(*testing.T, sharedCorpusCase){
		"putGetRoundTrip": validateObjectCorpusCase,
		"conditionalPut":  validateConditionalCorpusCase,
		"conditionalGet":  validateConditionalCorpusCase,
		"checksumPut":     validateChecksumCorpusCase,
		"listObjectsV2":   validateListCorpusCase,
		"copyObject":      validateCopyCorpusCase,
		"multipartUpload": validateMultipartCorpusCase,
	}
	validator, ok := validators[testCase.Operation]
	if !ok {
		t.Fatalf("corpus case %q has unsupported operation %q", testCase.ID, testCase.Operation)
	}
	validator(t, testCase)
}

func validateObjectCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	if testCase.Bucket == "" || testCase.Key == "" {
		t.Fatalf("corpus case %q requires bucket and key", testCase.ID)
	}
}

func validateConditionalCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	validateObjectCorpusCase(t, testCase)
	if testCase.IfMatch == "" && testCase.IfNoneMatch == "" {
		t.Fatalf("conditional case %q requires a condition", testCase.ID)
	}
}

func validateChecksumCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	validateObjectCorpusCase(t, testCase)
	if testCase.ContentMD5 == "" && (testCase.ChecksumAlgorithm == "" || testCase.ChecksumValue == "") {
		t.Fatalf("checksum case %q requires a checksum", testCase.ID)
	}
	if testCase.ChecksumAlgorithm != "" && !supportedCorpusChecksum(testCase.ChecksumAlgorithm) {
		t.Fatalf("checksum case %q has unsupported algorithm %q", testCase.ID, testCase.ChecksumAlgorithm)
	}
}

func validateListCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	if testCase.Bucket == "" || testCase.MaxKeys <= 0 || len(testCase.Expect.Pages) == 0 {
		t.Fatalf("list case %q requires bucket, maxKeys, and pages", testCase.ID)
	}
}

func validateCopyCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	if testCase.Bucket == "" || testCase.SourceBucket == "" || testCase.SourceKey == "" || testCase.DestinationKey == "" {
		t.Fatalf("copy case %q requires source and destination fields", testCase.ID)
	}
}

func validateMultipartCorpusCase(t *testing.T, testCase sharedCorpusCase) {
	t.Helper()
	if testCase.Bucket == "" || testCase.Key == "" || len(testCase.Parts) == 0 {
		t.Fatalf("multipart case %q requires bucket, key, and parts", testCase.ID)
	}
	for _, part := range testCase.Parts {
		if part.Number < 1 || part.Number > 10000 || part.Repeat < 0 {
			t.Fatalf("multipart case %q has invalid part", testCase.ID)
		}
	}
}
