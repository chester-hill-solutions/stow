//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"syscall/js"

	stowruntime "github.com/chester-hill-solutions/stow/internal/runtime"
)

type request struct {
	Op                string                  `json:"op"`
	Handle            int                     `json:"handle"`
	Options           stowruntime.Options     `json:"options"`
	Bucket            string                  `json:"bucket"`
	Key               string                  `json:"key"`
	Data              string                  `json:"data"`
	ContentType       string                  `json:"contentType"`
	Metadata          map[string]string       `json:"metadata"`
	List              stowruntime.ListOptions `json:"list"`
	SourceBucket      string                  `json:"sourceBucket"`
	SourceKey         string                  `json:"sourceKey"`
	DestinationBucket string                  `json:"destinationBucket"`
	DestinationKey    string                  `json:"destinationKey"`
}

type response struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type openResult struct {
	Handle       int                `json:"handle"`
	Capabilities capabilitiesResult `json:"capabilities"`
}

type capabilitiesResult struct {
	Backend    stowruntime.Backend `json:"backend"`
	MaxBytes   int64               `json:"maxBytes"`
	MaxObjects int64               `json:"maxObjects"`
	Persistent bool                `json:"persistent"`
	Multipart  bool                `json:"multipart"`
	Upstream   bool                `json:"upstream"`
}

type objectResult struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	Data         string            `json:"data,omitempty"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"contentType,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	LastModified string            `json:"lastModified,omitempty"`
}

type listResult struct {
	Objects []objectResult `json:"objects"`
}

type bucketResult struct {
	Name         string `json:"name"`
	CreationDate string `json:"creationDate,omitempty"`
}

type bucketListResult struct {
	Buckets []bucketResult `json:"buckets"`
}

type usageResult struct {
	Bytes   int64 `json:"bytes"`
	Objects int64 `json:"objects"`
}

type handler func(context.Context, *stowruntime.Instance, request) (json.RawMessage, error)

var state = struct {
	sync.Mutex
	next      int
	instances map[int]*stowruntime.Instance
}{instances: make(map[int]*stowruntime.Instance)}

func main() {
	state.Lock()
	state.next = 1
	state.Unlock()
	exit := make(chan struct{})
	var exitOnce sync.Once
	exitFunc := js.FuncOf(func(js.Value, []js.Value) interface{} {
		exitOnce.Do(func() { close(exit) })
		return nil
	})
	callFunc := js.FuncOf(call)
	js.Global().Set("stow", js.ValueOf(map[string]interface{}{"call": callFunc, "exit": exitFunc}))
	<-exit
}

func call(_ js.Value, args []js.Value) interface{} {
	if len(args) != 1 {
		return encodeResponse(response{Error: "expected one JSON request"})
	}
	var req request
	if err := json.Unmarshal([]byte(args[0].String()), &req); err != nil {
		return encodeResponse(response{Error: "invalid JSON request: " + err.Error()})
	}
	result, err := dispatch(req)
	if err != nil {
		return encodeResponse(response{Error: err.Error()})
	}
	return encodeResponse(response{OK: true, Result: result})
}

func dispatch(req request) (json.RawMessage, error) {
	state.Lock()
	defer state.Unlock()
	if req.Op == "open" {
		return openRuntime(req)
	}
	instance, ok := state.instances[req.Handle]
	if !ok {
		return nil, fmt.Errorf("runtime handle %d is not open", req.Handle)
	}
	operation, ok := operationHandlers[req.Op]
	if !ok {
		return nil, fmt.Errorf("unknown operation %q", req.Op)
	}
	return operation(backgroundContext(), instance, req)
}

func openRuntime(req request) (json.RawMessage, error) {
	instance, err := stowruntime.Open(req.Options)
	if err != nil {
		return nil, err
	}
	handle := state.next
	state.next++
	state.instances[handle] = instance
	return marshalResult(openResult{Handle: handle, Capabilities: capabilities(instance.Capabilities())})
}

func capabilities(value stowruntime.Capabilities) capabilitiesResult {
	return capabilitiesResult{
		Backend:    value.Backend,
		MaxBytes:   value.MaxBytes,
		MaxObjects: value.MaxObjects,
		Persistent: value.Persistent,
		Multipart:  value.Multipart,
		Upstream:   value.Upstream,
	}
}

func encodeResponse(value response) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"ok":false,"error":"response encoding failed"}`
	}
	return string(data)
}

func marshalResult(value interface{}) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func backgroundContext() context.Context {
	return context.Background()
}
