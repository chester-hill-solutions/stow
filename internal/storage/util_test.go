package storage

import (
	"testing"
	"time"
)

func TestValidBucketName(t *testing.T) {
	valid := []string{"abc", "a-b", "-abc", "abc-", "a_b", "a.b"}
	invalid := []string{"", "ab", "a/b", "a..b", "ABC", "a b", "a\\b", "a\nb"}
	for _, name := range valid {
		if !ValidBucketName(name) {
			t.Errorf("ValidBucketName(%q) = false, want true", name)
		}
	}
	for _, name := range invalid {
		if ValidBucketName(name) {
			t.Errorf("ValidBucketName(%q) = true, want false", name)
		}
	}
}

func TestValidateBucketNameRejectsIPAndReservedPrefixes(t *testing.T) {
	invalid := []string{
		"192.168.5.4",
		"127.0.0.1",
		"xn--bucket",
		"sthree-bucket",
		"amzn-s3-demo-bucket",
		"amzn_s3_demo_bucket",
	}
	for _, name := range invalid {
		if err := ValidateBucketName(name); err != ErrInvalidBucketName {
			t.Errorf("ValidateBucketName(%q) = %v, want ErrInvalidBucketName", name, err)
		}
	}
}

func TestPaginateObjects_DelimiterAndTruncation(t *testing.T) {
	now := time.Now().UTC()
	items := []ObjectMeta{
		{Key: "a/1.txt", Size: 1, LastModified: now},
		{Key: "a/2.txt", Size: 2, LastModified: now},
		{Key: "b.txt", Size: 3, LastModified: now},
		{Key: "c.txt", Size: 4, LastModified: now},
	}

	result := PaginateObjects(items, ListOptions{Delimiter: "/", MaxKeys: 1})
	if len(result.CommonPrefixes) != 1 || result.CommonPrefixes[0] != "a/" {
		t.Fatalf("common prefixes = %v", result.CommonPrefixes)
	}
	if len(result.Objects) != 0 {
		t.Fatalf("objects = %+v, want none", result.Objects)
	}
	if result.KeyCount != 1 {
		t.Fatalf("key count = %d, want 1", result.KeyCount)
	}
	if !result.IsTruncated || result.NextContinuationToken != "a/" {
		t.Fatalf("truncation = truncated=%v token=%q", result.IsTruncated, result.NextContinuationToken)
	}
}

func TestPaginateObjects_Continuation(t *testing.T) {
	items := []ObjectMeta{
		{Key: "a"},
		{Key: "b"},
		{Key: "c"},
	}
	result := PaginateObjects(items, ListOptions{ContinuationToken: "a", MaxKeys: 10})
	if len(result.Objects) != 2 || result.Objects[0].Key != "b" || result.Objects[1].Key != "c" {
		t.Fatalf("objects = %+v", result.Objects)
	}
	if result.ContinuationToken != "a" {
		t.Fatalf("ContinuationToken = %q", result.ContinuationToken)
	}
}
