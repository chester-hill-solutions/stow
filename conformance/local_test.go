package conformance_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestPutGetRoundtrip(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	key := "hello.txt"
	body := []byte("hello conformance")

	createBucket(ctx, t, env.Client, bucket)

	putOut, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("text/plain"),
		Metadata: map[string]string{
			"origin": "test",
		},
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if putOut.ETag == nil || *putOut.ETag == "" {
		t.Fatal("expected ETag on PutObject")
	}

	headOut, err := env.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if aws.ToInt64(headOut.ContentLength) != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", aws.ToInt64(headOut.ContentLength), len(body))
	}
	if aws.ToString(headOut.ContentType) != "text/plain" {
		t.Fatalf("ContentType = %q", aws.ToString(headOut.ContentType))
	}
	if aws.ToString(headOut.ETag) != aws.ToString(putOut.ETag) {
		t.Fatalf("Head ETag %q != Put ETag %q", aws.ToString(headOut.ETag), aws.ToString(putOut.ETag))
	}
	if headOut.Metadata["origin"] != "test" {
		t.Fatalf("metadata origin = %q", headOut.Metadata["origin"])
	}

	getOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getOut.Body.Close()
	got, err := io.ReadAll(getOut.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if getOut.Metadata["origin"] != "test" {
		t.Fatalf("get metadata origin = %q", getOut.Metadata["origin"])
	}

	_, err = env.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
}

func TestPutObjectConditionalWrite(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "conditional")
	createBucket(ctx, t, env.Client, bucket)

	first, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("key"),
		Body:   strings.NewReader("first"),
	})
	if err != nil {
		t.Fatalf("first PutObject: %v", err)
	}
	_, err = env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String("key"),
		Body:        strings.NewReader("second"),
		IfNoneMatch: first.ETag,
	})
	if err == nil {
		t.Fatal("expected conditional write failure")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %v", err)
	}
}

func TestPutObjectRejectsWrongContentMD5(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "checksum")
	createBucket(ctx, t, env.Client, bucket)
	bad := base64.StdEncoding.EncodeToString([]byte("not-the-body"))
	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("key"),
		Body:       strings.NewReader("body"),
		ContentMD5: aws.String(bad),
	})
	if err == nil {
		t.Fatal("expected checksum mismatch")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400, got %v", err)
	}
}

func TestListObjectsV2Prefix(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	createBucket(ctx, t, env.Client, bucket)

	keys := []string{"a/1", "a/2", "b/1"}
	for _, key := range keys {
		_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader(key),
		})
		if err != nil {
			t.Fatalf("PutObject(%q): %v", key, err)
		}
	}

	out, err := env.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("a/"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(out.Contents) != 2 {
		t.Fatalf("expected 2 objects with prefix a/, got %d", len(out.Contents))
	}
	for _, obj := range out.Contents {
		k := aws.ToString(obj.Key)
		if !strings.HasPrefix(k, "a/") {
			t.Fatalf("unexpected key %q in prefix listing", k)
		}
	}
}

func TestDeleteObject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	key := "delete-me"
	createBucket(ctx, t, env.Client, bucket)

	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader("bye"),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	_, err = env.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// Idempotent delete
	_, err = env.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("idempotent DeleteObject: %v", err)
	}
}

func TestCopyObject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	srcBucket := uniqueBucket(t, "src")
	dstBucket := uniqueBucket(t, "dst")
	createBucket(ctx, t, env.Client, srcBucket)
	createBucket(ctx, t, env.Client, dstBucket)

	body := []byte("original bytes")
	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(srcBucket),
		Key:    aws.String("original"),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("PutObject source: %v", err)
	}

	copyOut, err := env.Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		Key:        aws.String("copy"),
		CopySource: aws.String(srcBucket + "/original"),
	})
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if copyOut.CopyObjectResult == nil || copyOut.CopyObjectResult.ETag == nil {
		t.Fatal("expected ETag on CopyObject")
	}

	getOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(dstBucket),
		Key:    aws.String("copy"),
	})
	if err != nil {
		t.Fatalf("GetObject copy: %v", err)
	}
	defer getOut.Body.Close()
	got, _ := io.ReadAll(getOut.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("copy body = %q, want %q", got, body)
	}
}

func TestCopyObjectReplaceMetadata(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	srcBucket := uniqueBucket(t, "src")
	dstBucket := uniqueBucket(t, "dst")
	createBucket(ctx, t, env.Client, srcBucket)
	createBucket(ctx, t, env.Client, dstBucket)

	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(srcBucket),
		Key:         aws.String("source"),
		Body:        strings.NewReader("copy me"),
		ContentType: aws.String("text/plain"),
		Metadata:    map[string]string{"origin": "source"},
	})
	if err != nil {
		t.Fatalf("PutObject source: %v", err)
	}
	_, err = env.Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(dstBucket),
		Key:               aws.String("replacement"),
		CopySource:        aws.String(srcBucket + "/source"),
		MetadataDirective: types.MetadataDirectiveReplace,
		ContentType:       aws.String("application/json"),
		Metadata:          map[string]string{"origin": "replacement"},
	})
	if err != nil {
		t.Fatalf("CopyObject replace: %v", err)
	}

	head, err := env.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(dstBucket),
		Key:    aws.String("replacement"),
	})
	if err != nil {
		t.Fatalf("HeadObject replacement: %v", err)
	}
	if got := aws.ToString(head.ContentType); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	if got := head.Metadata["origin"]; got != "replacement" {
		t.Fatalf("metadata origin = %q, want replacement", got)
	}
}

func TestCopyObjectPreconditionFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "precondition")
	createBucket(ctx, t, env.Client, bucket)
	put, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("source"),
		Body:   strings.NewReader("source"),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	_, err = env.Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:                aws.String(bucket),
		Key:                   aws.String("copy"),
		CopySource:            aws.String(bucket + "/source"),
		CopySourceIfNoneMatch: put.ETag,
	})
	if err == nil {
		t.Fatal("expected copy precondition failure")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %v", err)
	}
}

func TestRangeGetObject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	key := "range.bin"
	createBucket(ctx, t, env.Client, bucket)

	const size = 10 * 1024
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 256)
	}
	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	rangeOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String("bytes=0-1023"),
	})
	if err != nil {
		t.Fatalf("GetObject range 0-1023: %v", err)
	}
	defer rangeOut.Body.Close()
	if aws.ToInt64(rangeOut.ContentLength) != 1024 {
		t.Fatalf("ContentLength = %d, want 1024", aws.ToInt64(rangeOut.ContentLength))
	}
	if cr := aws.ToString(rangeOut.ContentRange); !strings.HasPrefix(cr, "bytes 0-1023/") {
		t.Fatalf("ContentRange = %q", cr)
	}
	got, _ := io.ReadAll(rangeOut.Body)
	if !bytes.Equal(got, body[:1024]) {
		t.Fatal("range body mismatch")
	}

	suffixOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String("bytes=-512"),
	})
	if err != nil {
		t.Fatalf("GetObject suffix range: %v", err)
	}
	defer suffixOut.Body.Close()
	suffix, _ := io.ReadAll(suffixOut.Body)
	if !bytes.Equal(suffix, body[size-512:]) {
		t.Fatal("suffix range body mismatch")
	}

	_, err = env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String("bytes=99999-"),
	})
	if err == nil {
		t.Fatal("expected error for unsatisfiable range")
	}
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %v", err)
	}
}

func TestListMultipartUploads(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "uploads")
	createBucket(ctx, t, env.Client, bucket)

	created, err := env.Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("large/object.bin"),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := aws.ToString(created.UploadId)
	if uploadID == "" {
		t.Fatal("expected upload ID")
	}
	t.Cleanup(func() {
		_, _ = env.Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String("large/object.bin"),
			UploadId: aws.String(uploadID),
		})
	})

	out, err := env.Client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("ListMultipartUploads: %v", err)
	}
	if len(out.Uploads) != 1 {
		t.Fatalf("expected one in-progress upload, got %d", len(out.Uploads))
	}
	if got := aws.ToString(out.Uploads[0].Key); got != "large/object.bin" {
		t.Fatalf("upload key = %q, want %q", got, "large/object.bin")
	}
	if got := aws.ToString(out.Uploads[0].UploadId); got != uploadID {
		t.Fatalf("upload ID = %q, want %q", got, uploadID)
	}
}

func TestMultipartUpload(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	key := "large.bin"
	createBucket(ctx, t, env.Client, bucket)

	createOut, err := env.Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := aws.ToString(createOut.UploadId)
	if uploadID == "" {
		t.Fatal("empty upload id")
	}

	const minPart = 5 * 1024 * 1024
	part1 := make([]byte, minPart)
	for i := range part1 {
		part1[i] = 'A'
	}
	part2 := []byte("final-part")

	up1, err := env.Client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(1),
		Body:       bytes.NewReader(part1),
	})
	if err != nil {
		t.Fatalf("UploadPart 1: %v", err)
	}

	up2, err := env.Client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(2),
		Body:       bytes.NewReader(part2),
	})
	if err != nil {
		t.Fatalf("UploadPart 2: %v", err)
	}

	listOut, err := env.Client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		t.Fatalf("ListParts: %v", err)
	}
	if len(listOut.Parts) != 2 {
		t.Fatalf("ListParts count = %d, want 2", len(listOut.Parts))
	}

	completeOut, err := env.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: []types.CompletedPart{
				{ETag: up1.ETag, PartNumber: aws.Int32(1)},
				{ETag: up2.ETag, PartNumber: aws.Int32(2)},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	if completeOut.ETag == nil || *completeOut.ETag == "" {
		t.Fatal("expected composite ETag")
	}
	part1Hash := md5.Sum(part1)
	part2Hash := md5.Sum(part2)
	combinedHash := md5.Sum(append(append([]byte{}, part1Hash[:]...), part2Hash[:]...))
	wantETag := fmt.Sprintf("\"%s-2\"", hex.EncodeToString(combinedHash[:]))
	if got := aws.ToString(completeOut.ETag); got != wantETag {
		t.Fatalf("composite ETag = %q, want %q", got, wantETag)
	}

	getOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getOut.Body.Close()
	data, _ := io.ReadAll(getOut.Body)
	expectedLen := minPart + len(part2)
	if len(data) != expectedLen {
		t.Fatalf("object size = %d, want %d", len(data), expectedLen)
	}
	if !bytes.Equal(data[:minPart], part1) || !bytes.Equal(data[minPart:], part2) {
		t.Fatal("multipart object bytes mismatch")
	}
}

func TestCompleteMultipartRejectsWrongETag(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "wrong-etag")
	createBucket(ctx, t, env.Client, bucket)
	created, err := env.Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("object"),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := aws.ToString(created.UploadId)
	part, err := env.Client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("object"),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(1),
		Body:       strings.NewReader("body"),
	})
	if err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	_, err = env.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String("object"),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: []types.CompletedPart{{ETag: aws.String("\"wrong\""), PartNumber: aws.Int32(1)}},
		},
	})
	if err == nil {
		t.Fatal("expected wrong ETag completion to fail")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400, got %v", err)
	}
	_, _ = env.Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("object"), UploadId: aws.String(uploadID),
	})
	_ = part
}

func TestPresignedGetPut(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	getKey := "presign-get.txt"
	putKey := "presign-put.txt"
	createBucket(ctx, t, env.Client, bucket)

	seed := []byte("presigned download")
	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(getKey),
		Body:   bytes.NewReader(seed),
	})
	if err != nil {
		t.Fatalf("seed PutObject: %v", err)
	}

	getReq, err := env.Presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(getKey),
	}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		t.Fatalf("PresignGetObject: %v", err)
	}

	resp, err := http.Get(getReq.URL)
	if err != nil {
		t.Fatalf("presigned GET fetch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, seed) {
		t.Fatalf("presigned GET body = %q", got)
	}

	putBody := []byte("presigned upload")
	putReq, err := env.Presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(putKey),
		ContentType: aws.String("text/plain"),
	}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		t.Fatalf("PresignPutObject: %v", err)
	}

	putHTTP, err := http.NewRequestWithContext(ctx, http.MethodPut, putReq.URL, bytes.NewReader(putBody))
	if err != nil {
		t.Fatalf("put request: %v", err)
	}
	putHTTP.Header.Set("Content-Type", "text/plain")
	putHTTP.ContentLength = int64(len(putBody))
	putResp, err := http.DefaultClient.Do(putHTTP)
	if err != nil {
		t.Fatalf("presigned PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT status = %d", putResp.StatusCode)
	}

	_, err = env.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(putKey),
	})
	if err != nil {
		t.Fatalf("HeadObject after presigned PUT: %v", err)
	}
}

func TestDeleteObjects(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	createBucket(ctx, t, env.Client, bucket)

	keys := []string{"batch-1", "batch-2", "batch-3"}
	for _, key := range keys {
		_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader(key),
		})
		if err != nil {
			t.Fatalf("PutObject(%q): %v", key, err)
		}
	}

	delOut, err := env.Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String("batch-1")},
				{Key: aws.String("batch-2")},
			},
			Quiet: aws.Bool(false),
		},
	})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if len(delOut.Deleted) != 2 {
		t.Fatalf("deleted count = %d, want 2", len(delOut.Deleted))
	}

	listOut, err := env.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(listOut.Contents) != 1 {
		t.Fatalf("remaining keys = %d, want 1", len(listOut.Contents))
	}
	if aws.ToString(listOut.Contents[0].Key) != "batch-3" {
		t.Fatalf("remaining key = %q", aws.ToString(listOut.Contents[0].Key))
	}
}

func TestPathStyleRequired(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t)
	key := "style.txt"
	createBucket(ctx, t, env.Client, bucket)

	_, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader("path-style"),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	getOut, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer getOut.Body.Close()
	got, _ := io.ReadAll(getOut.Body)
	if string(got) != "path-style" {
		t.Fatalf("body = %q", got)
	}
}

// TestSigV4RejectsUnsigned verifies DevBypass is not in effect.
func TestSigV4RejectsUnsigned(t *testing.T) {
	env := newTestEnv(t)
	bucket := uniqueBucket(t)

	resp, err := http.Get(fmt.Sprintf("%s/%s", env.Endpoint, bucket))
	if err != nil {
		t.Fatalf("unsigned GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned request status = %d, want 403", resp.StatusCode)
	}
}
