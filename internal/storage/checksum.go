package storage

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"strings"
)

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// ComputeChecksum returns the S3 base64 checksum for a supported algorithm.
func ComputeChecksum(algorithm string, data []byte) (string, error) {
	var sum []byte
	switch strings.ToUpper(strings.TrimSpace(algorithm)) {
	case "CRC32":
		value := crc32.ChecksumIEEE(data)
		sum = []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
	case "CRC32C":
		value := crc32.Checksum(data, crc32cTable)
		sum = []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
	case "SHA1":
		digest := sha1.Sum(data)
		sum = digest[:]
	case "SHA256":
		digest := sha256.Sum256(data)
		sum = digest[:]
	default:
		return "", fmt.Errorf("unsupported checksum algorithm %q", algorithm)
	}
	return base64.StdEncoding.EncodeToString(sum), nil
}

func NormalizeChecksumAlgorithm(algorithm string) string {
	return strings.ToUpper(strings.TrimSpace(algorithm))
}
