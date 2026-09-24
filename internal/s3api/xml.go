package s3api

import (
	"encoding/xml"
	"time"
)

type listBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   owner    `xml:"Owner"`
	Buckets buckets  `xml:"Buckets"`
}

type owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type buckets struct {
	Items []bucketEntry `xml:"Bucket"`
}

type bucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type listBucketResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	IsTruncated           bool           `xml:"IsTruncated"`
	Contents              []objectEntry  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	EncodingType          string         `xml:"EncodingType,omitempty"`
}

type objectEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type putObjectResult struct {
	XMLName xml.Name `xml:"PutObjectResult"`
	ETag    string   `xml:"ETag"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type deleteResult struct {
	XMLName xml.Name       `xml:"DeleteResult"`
	Deleted []deletedEntry `xml:"Deleted"`
	Errors  []errorEntry   `xml:"Error"`
}

type deletedEntry struct {
	Key string `xml:"Key"`
}

type errorEntry struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
	Location string   `xml:"Location"`
}

type listPartsResult struct {
	XMLName              xml.Name    `xml:"ListPartsResult"`
	Bucket               string      `xml:"Bucket"`
	Key                  string      `xml:"Key"`
	UploadID             string      `xml:"UploadId"`
	PartNumberMarker     int         `xml:"PartNumberMarker"`
	NextPartNumberMarker int         `xml:"NextPartNumberMarker,omitempty"`
	MaxParts             int         `xml:"MaxParts"`
	IsTruncated          bool        `xml:"IsTruncated"`
	Parts                []partEntry `xml:"Part"`
}

type partEntry struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName            xml.Name             `xml:"ListMultipartUploadsResult"`
	Xmlns              string               `xml:"xmlns,attr"`
	Bucket             string               `xml:"Bucket"`
	KeyMarker          string               `xml:"KeyMarker,omitempty"`
	UploadIDMarker     string               `xml:"UploadIdMarker,omitempty"`
	NextKeyMarker      string               `xml:"NextKeyMarker,omitempty"`
	NextUploadIDMarker string               `xml:"NextUploadIdMarker,omitempty"`
	Prefix             string               `xml:"Prefix"`
	Delimiter          string               `xml:"Delimiter,omitempty"`
	MaxUploads         int                  `xml:"MaxUploads"`
	IsTruncated        bool                 `xml:"IsTruncated"`
	Uploads            []multipartUploadXML `xml:"Upload"`
}

type multipartUploadXML struct {
	Key       string `xml:"Key"`
	UploadID  string `xml:"UploadId"`
	Initiated string `xml:"Initiated"`
}

type uploadPartResult struct {
	XMLName xml.Name `xml:"UploadPartResult"`
	ETag    string   `xml:"ETag"`
}

type deleteObjectsRequest struct {
	XMLName xml.Name           `xml:"Delete"`
	Quiet   bool               `xml:"Quiet"`
	Objects []deleteObjectItem `xml:"Object"`
}

type deleteObjectItem struct {
	Key string `xml:"Key"`
}

type completeMultipartUploadRequest struct {
	XMLName xml.Name            `xml:"CompleteMultipartUpload"`
	Parts   []completePartEntry `xml:"Part"`
}

type completePartEntry struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

func newListBucketsResult(items []bucketEntry) listBucketsResult {
	return listBucketsResult{
		Xmlns:   xmlNS,
		Owner:   owner{ID: "stow", DisplayName: "stow"},
		Buckets: buckets{Items: items},
	}
}

func objectToEntry(o objectMeta, encodeURL bool) objectEntry {
	key := o.Key
	if encodeURL {
		key = urlEncodeKey(key)
	}
	return objectEntry{
		Key:          key,
		LastModified: formatTime(o.LastModified),
		ETag:         o.ETag,
		Size:         o.Size,
		StorageClass: "STANDARD",
	}
}

// objectMeta mirrors storage.ObjectMeta without importing in xml layer tests.
type objectMeta struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}
