package runthrough

import "errors"

// ErrLiveWritesDisabled is returned when a mutating upstream operation is
// attempted while allowLiveWrites is false.
var ErrLiveWritesDisabled = errors.New("live writes disabled: set allowLiveWrites to propagate mutations to upstream")
