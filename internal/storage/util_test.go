package storage

import (
	"testing"
	"time"
)

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
	if len(result.Objects) != 1 || result.Objects[0].Key != "b.txt" {
		t.Fatalf("objects = %+v", result.Objects)
	}
	if !result.IsTruncated || result.NextContinuationToken != "c.txt" {
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
