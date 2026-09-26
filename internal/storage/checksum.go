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

// VerifyChecksum checks a caller-supplied checksum against the body it was
// supplied for, and returns ErrChecksumMismatch when they disagree.
//
// It is package-level rather than a method on one store because verifying an
// integrity claim is part of the object model, and it had ended up implemented
// in exactly one of the three stores: workspace verified, memory and filesystem
// stored the algorithm and value verbatim and accepted a corrupt body. The
// contract test that now covers this is why that cannot recur silently.
//
// A caller who supplies neither an algorithm nor a value has made no claim, so
// there is nothing to verify. Supplying one without the other is an error
// rather than a silent pass: storing a body whose checksum was never computed
// would advertise an integrity property the store does not have.
func VerifyChecksum(opts PutOptions, data []byte) error {
	algorithm := NormalizeChecksumAlgorithm(opts.ChecksumAlgorithm)
	if algorithm == "" && opts.ChecksumValue == "" {
		return nil
	}
	if algorithm == "" || opts.ChecksumValue == "" {
		return fmt.Errorf("checksum algorithm and value must be supplied together")
	}
	computed, err := ComputeChecksum(algorithm, data)
	if err != nil {
		return err
	}
	if computed != opts.ChecksumValue {
		return ErrChecksumMismatch
	}
	return nil
}
