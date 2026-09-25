// Package ready defines the machine-readable session readiness protocol.
//
// It exists so that every language client consumes one defined message instead
// of parsing the legacy human-readable STOW_READY line. The JSON message is
// written to a caller-supplied file descriptor so credentials do not travel on
// stdout, where they are easily captured by logs and CI output.
package ready

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// ProtocolVersion is the version of this message shape. Clients reject an
// unknown major version with a specific error rather than guessing.
const ProtocolVersion = 1

// Capabilities is what a caller can rely on before issuing operations.
type Capabilities struct {
	Persistent        bool  `json:"persistent"`
	Multipart         bool  `json:"multipart"`
	Upstream          bool  `json:"upstream"`
	ConditionalWrites bool  `json:"conditionalWrites"`
	PresignedURLs     bool  `json:"presignedUrls"`
	MaxBytes          int64 `json:"maxBytes"`
	MaxObjects        int64 `json:"maxObjects"`
	MaxRequestBytes   int64 `json:"maxRequestBytes"`
}

// Message is the single object written to the ready descriptor.
type Message struct {
	ProtocolVersion int          `json:"protocolVersion"`
	BinaryVersion   string       `json:"binaryVersion"`
	Endpoint        string       `json:"endpoint"`
	Region          string       `json:"region"`
	AccessKeyID     string       `json:"accessKeyId"`
	SecretAccessKey string       `json:"secretAccessKey"`
	Mode            string       `json:"mode"`
	Backend         string       `json:"backend"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ReportedLimit converts a configured limit into its wire form.
//
// JSON numbers cannot represent the int64 maximum exactly: 1<<63-1 decodes as
// 9223372036854776000, which is a different value from the one the server is
// enforcing. "No limit" is therefore reported as 0, matching the CLI flag
// convention that 0 disables a limit, so a client never sees a silently
// corrupted number.
func ReportedLimit(limit int64) int64 {
	if limit <= 0 || limit == math.MaxInt64 {
		return 0
	}
	return limit
}

// Input is what the CLI knows when the server is listening.
type Input struct {
	Endpoint      string
	Region        string
	AccessKeyID   string
	SecretKey     string
	Mode          string
	Backend       string
	BinaryVersion string
	Capabilities  Capabilities
}

// New builds the protocol message.
func New(input Input) Message {
	return Message{
		ProtocolVersion: ProtocolVersion,
		BinaryVersion:   input.BinaryVersion,
		Endpoint:        input.Endpoint,
		Region:          input.Region,
		AccessKeyID:     input.AccessKeyID,
		SecretAccessKey: input.SecretKey,
		Mode:            input.Mode,
		Backend:         input.Backend,
		Capabilities:    input.Capabilities,
	}
}

// WriteToFd writes exactly one JSON object to an inherited file descriptor.
//
// os.NewFile is used rather than a /dev/fd path so this works on macOS as well
// as Linux. The descriptor is not closed: the client owns the pipe and may read
// more than the first line. Arbitrary descriptor inheritance is a Unix
// facility, which matches the supported platforms for this release.
func WriteToFd(fd int, message Message) error {
	if fd < 0 {
		return fmt.Errorf("ready: file descriptor must not be negative")
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("ready: encode: %w", err)
	}
	file := os.NewFile(uintptr(fd), "stow-ready")
	if file == nil {
		return fmt.Errorf("ready: file descriptor %d is not open", fd)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("ready: write to fd %d: %w", fd, err)
	}
	return nil
}
