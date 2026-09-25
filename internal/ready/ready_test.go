package ready_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/ready"
)

func sampleMessage() ready.Message {
	return ready.New(ready.Input{
		Endpoint:      "http://127.0.0.1:43127",
		Region:        "us-east-1",
		AccessKeyID:   "generated-access-key",
		SecretKey:     "generated-secret-key",
		Mode:          "local",
		Backend:       "memory",
		BinaryVersion: "0.2.0",
		Capabilities: ready.Capabilities{
			Multipart:         true,
			ConditionalWrites: true,
			PresignedURLs:     true,
			MaxBytes:          16 << 20,
			MaxObjects:        1000,
			MaxRequestBytes:   8 << 20,
		},
	})
}

func TestMessageCarriesProtocolAndCapabilities(t *testing.T) {
	message := sampleMessage()
	if message.ProtocolVersion != ready.ProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", message.ProtocolVersion, ready.ProtocolVersion)
	}
	if message.Capabilities.Persistent {
		t.Fatal("a memory backend must not report persistent")
	}
	if !message.Capabilities.Multipart || !message.Capabilities.ConditionalWrites {
		t.Fatalf("capabilities = %+v", message.Capabilities)
	}
	if message.Capabilities.MaxRequestBytes == 0 {
		t.Fatal("capabilities must report a per-request limit")
	}
}

// The int64 maximum cannot survive a JSON round trip, so an unlimited limit is
// reported as 0 rather than as a silently corrupted number.
func TestReportedLimitNeverLosesPrecision(t *testing.T) {
	if got := ready.ReportedLimit(math.MaxInt64); got != 0 {
		t.Fatalf("ReportedLimit(MaxInt64) = %d, want 0", got)
	}
	if got := ready.ReportedLimit(0); got != 0 {
		t.Fatalf("ReportedLimit(0) = %d, want 0", got)
	}
	if got := ready.ReportedLimit(-1); got != 0 {
		t.Fatalf("ReportedLimit(-1) = %d, want 0", got)
	}
	if got := ready.ReportedLimit(16 << 20); got != 16<<20 {
		t.Fatalf("ReportedLimit(16MiB) = %d, want %d", got, 16<<20)
	}

	encoded, err := json.Marshal(map[string]int64{"maxBytes": ready.ReportedLimit(math.MaxInt64)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "9223372036854776") {
		t.Fatalf("unlimited limit leaked an imprecise number: %s", encoded)
	}
}

func TestWriteToFdEmitsOneJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := file.WriteString(""); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := ready.WriteToFd(int(file.Fd()), sampleMessage()); err != nil {
		t.Fatalf("write to fd: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrote %d lines, want 1: %q", len(lines), data)
	}
	var decoded ready.Message
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	if decoded.Endpoint != "http://127.0.0.1:43127" || decoded.AccessKeyID != "generated-access-key" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Capabilities.MaxObjects != 1000 {
		t.Fatalf("capabilities = %+v", decoded.Capabilities)
	}
}

func TestWriteToFdRejectsInvalidDescriptor(t *testing.T) {
	if err := ready.WriteToFd(-1, sampleMessage()); err == nil {
		t.Fatal("expected a negative descriptor to be rejected")
	}
}
