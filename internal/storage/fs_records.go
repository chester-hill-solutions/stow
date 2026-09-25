package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const objectRecordVersion = 1

type objectRecord struct {
	Version           int               `json:"version"`
	RecordVersion     string            `json:"record_version,omitempty"`
	Data              []byte            `json:"data"`
	ContentType       string            `json:"content_type,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	ETag              string            `json:"etag"`
	ChecksumAlgorithm string            `json:"checksum_algorithm,omitempty"`
	ChecksumValue     string            `json:"checksum_value,omitempty"`
	LastModified      time.Time         `json:"last_modified"`
}

func writeObjectRecord(path string, record objectRecord) error {
	record.Version = objectRecordVersion
	return writeJSONAtomic(path, record)
}

func readObjectRecord(path string) (objectRecord, error) {
	var record objectRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("decode object record: %w", err)
	}
	if record.Version != objectRecordVersion {
		return record, fmt.Errorf("unsupported object record version %d", record.Version)
	}
	if record.Data == nil {
		record.Data = []byte{}
	}
	return record, nil
}

func (r objectRecord) meta(bucket, key string) ObjectMeta {
	versionID := r.RecordVersion
	if versionID == "" {
		versionID = r.ETag
	}
	return ObjectMeta{
		Bucket:            bucket,
		Key:               key,
		VersionID:         versionID,
		Size:              int64(len(r.Data)),
		ETag:              r.ETag,
		ContentType:       r.ContentType,
		LastModified:      r.LastModified.UTC(),
		Metadata:          cloneMetadata(r.Metadata),
		ChecksumAlgorithm: r.ChecksumAlgorithm,
		ChecksumValue:     r.ChecksumValue,
	}
}
