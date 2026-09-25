package runthrough

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

// Client reads and writes objects against upstream S3-compatible storage.
type Client interface {
	HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error)
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error)
	PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) error
	DeleteObject(ctx context.Context, bucket, key string) error
	ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error)
}

// S3Client implements Client with the AWS SDK for Go v2.
type S3Client struct {
	s3     *s3.Client
	region string
}

// NewS3Client builds an upstream client for a custom S3-compatible endpoint.
func NewS3Client(cfg UpstreamConfig) (*S3Client, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKey,
			cfg.SecretKey,
			cfg.SessionToken,
		)),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = true
	})

	return &S3Client{s3: client, region: region}, nil
}

func (c *S3Client) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, mapUpstreamError(err)
	}
	return headOutputToMeta(bucket, key, out), nil
}

func (c *S3Client) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, nil, mapUpstreamError(err)
	}
	meta := getOutputToMeta(bucket, key, out)
	return out.Body, meta, nil
}

func (c *S3Client) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) error {
	if body == nil {
		return errors.New("upstream put body is nil")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}
	if opts.ChecksumAlgorithm != "" {
		input.ChecksumAlgorithm = types.ChecksumAlgorithm(opts.ChecksumAlgorithm)
		if opts.ChecksumValue != "" {
			switch opts.ChecksumAlgorithm {
			case "CRC32":
				input.ChecksumCRC32 = aws.String(opts.ChecksumValue)
			case "CRC32C":
				input.ChecksumCRC32C = aws.String(opts.ChecksumValue)
			case "SHA1":
				input.ChecksumSHA1 = aws.String(opts.ChecksumValue)
			case "SHA256":
				input.ChecksumSHA256 = aws.String(opts.ChecksumValue)
			}
		}
	}
	_, err = c.s3.PutObject(ctx, input)
	return mapUpstreamError(err)
}

func (c *S3Client) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return mapUpstreamError(err)
}

func (c *S3Client) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	}
	if opts.Prefix != "" {
		input.Prefix = aws.String(opts.Prefix)
	}
	if opts.Delimiter != "" {
		input.Delimiter = aws.String(opts.Delimiter)
	}
	if opts.MaxKeys > 0 {
		input.MaxKeys = aws.Int32(int32(opts.MaxKeys))
	}
	if opts.ContinuationToken != "" {
		input.ContinuationToken = aws.String(opts.ContinuationToken)
	}
	if opts.StartAfter != "" {
		input.StartAfter = aws.String(opts.StartAfter)
	}

	out, err := c.s3.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, mapUpstreamError(err)
	}

	result := &storage.ListResult{
		IsTruncated:           aws.ToBool(out.IsTruncated),
		ContinuationToken:     aws.ToString(out.ContinuationToken),
		NextContinuationToken: aws.ToString(out.NextContinuationToken),
	}
	for _, obj := range out.Contents {
		result.Objects = append(result.Objects, objectToMeta(bucket, obj))
	}
	for _, cp := range out.CommonPrefixes {
		if p := aws.ToString(cp.Prefix); p != "" {
			result.CommonPrefixes = append(result.CommonPrefixes, p)
		}
	}
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	return result, nil
}

func mapUpstreamError(err error) error {
	if err == nil {
		return nil
	}
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		return storage.ErrObjectNotFound
	}
	var noBucket *types.NoSuchBucket
	if errors.As(err, &noBucket) {
		return storage.ErrBucketNotFound
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr != nil && respErr.Response != nil && respErr.HTTPStatusCode() == 404 {
		return storage.ErrObjectNotFound
	}
	class := classifyRetry(err)
	if class == RetryClassUnknown {
		return err
	}
	return &UpstreamError{
		Err:        err,
		StatusCode: upstreamHTTPStatus(err),
		Code:       upstreamErrorCode(err),
		Class:      class,
	}
}

func headOutputToMeta(bucket, key string, out *s3.HeadObjectOutput) *storage.ObjectMeta {
	meta := &storage.ObjectMeta{
		Bucket:      bucket,
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ETag:        normalizeETag(aws.ToString(out.ETag)),
		ContentType: aws.ToString(out.ContentType),
		Metadata:    cloneStringMap(out.Metadata),
	}
	applyUpstreamChecksum(meta, out.ChecksumCRC32, out.ChecksumCRC32C, out.ChecksumSHA1, out.ChecksumSHA256)
	if out.LastModified != nil {
		meta.LastModified = out.LastModified.UTC()
	}
	return meta
}

func getOutputToMeta(bucket, key string, out *s3.GetObjectOutput) *storage.ObjectMeta {
	meta := &storage.ObjectMeta{
		Bucket:      bucket,
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ETag:        normalizeETag(aws.ToString(out.ETag)),
		ContentType: aws.ToString(out.ContentType),
		Metadata:    cloneStringMap(out.Metadata),
	}
	applyUpstreamChecksum(meta, out.ChecksumCRC32, out.ChecksumCRC32C, out.ChecksumSHA1, out.ChecksumSHA256)
	if out.LastModified != nil {
		meta.LastModified = out.LastModified.UTC()
	}
	return meta
}

func objectToMeta(bucket string, obj types.Object) storage.ObjectMeta {
	meta := storage.ObjectMeta{
		Bucket: bucket,
		Key:    aws.ToString(obj.Key),
		Size:   aws.ToInt64(obj.Size),
		ETag:   normalizeETag(aws.ToString(obj.ETag)),
	}
	if obj.LastModified != nil {
		meta.LastModified = obj.LastModified.UTC()
	}
	return meta
}

func normalizeETag(etag string) string {
	etag = strings.TrimSpace(etag)
	if etag == "" {
		return etag
	}
	if strings.HasPrefix(etag, "\"") && strings.HasSuffix(etag, "\"") {
		return etag
	}
	return "\"" + strings.Trim(etag, "\"") + "\""
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func applyUpstreamChecksum(meta *storage.ObjectMeta, crc32Value, crc32cValue, sha1Value, sha256Value *string) {
	for _, candidate := range []struct {
		algorithm string
		value     *string
	}{
		{algorithm: "CRC32", value: crc32Value},
		{algorithm: "CRC32C", value: crc32cValue},
		{algorithm: "SHA1", value: sha1Value},
		{algorithm: "SHA256", value: sha256Value},
	} {
		if candidate.value != nil && aws.ToString(candidate.value) != "" {
			meta.ChecksumAlgorithm = candidate.algorithm
			meta.ChecksumValue = aws.ToString(candidate.value)
			return
		}
	}
}

// upstreamChanged reports whether the upstream validator differs from the cached
// validator. ETag is a change detector here, not an ordering primitive.
func upstreamChanged(upstream, local *storage.ObjectMeta) bool {
	if upstream == nil {
		return false
	}
	if local == nil {
		return true
	}
	upETag := normalizeETag(upstream.ETag)
	localETag := normalizeETag(local.ETag)
	if upETag != "" && localETag != "" && upETag != localETag {
		return true
	}
	if !upstream.LastModified.IsZero() && !local.LastModified.IsZero() {
		return upstream.LastModified.After(local.LastModified)
	}
	return false
}
