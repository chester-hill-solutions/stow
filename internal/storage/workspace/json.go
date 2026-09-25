package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// The upload state is the only thing stow keeps as loose JSON, and it is
// stow's own private bookkeeping rather than a contract. It is typed here
// rather than marshalled through `any`, so a change to the shape is a change to
// one named type instead of a runtime surprise somewhere in a decode.
//
// The manifest is deliberately *not* handled here: it has a version number and
// a refusal path, which is a contract rather than bookkeeping.

// encodeUploadState marshals an in-progress upload's record.
func encodeUploadState(state *uploadState) ([]byte, error) {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("workspace store: encode upload: %w", err)
	}
	return raw, nil
}

// decodeUploadState reads an in-progress upload's record.
func decodeUploadState(raw []byte) (*uploadState, error) {
	state := &uploadState{}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, err
	}
	return state, nil
}

// detectContentTypeFromBytes sniffs a type from bytes already in hand, for the
// multipart completion path where the assembled object has not been written yet.
func detectContentTypeFromBytes(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return http.DetectContentType(head)
}
