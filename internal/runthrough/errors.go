package runthrough

import "errors"

// ErrLiveWritesDisabled is returned when a mutating upstream operation is
// attempted while allowLiveWrites is false.
var ErrLiveWritesDisabled = errors.New("live writes disabled: set allowLiveWrites to propagate mutations to upstream")

// ErrDurableOutboxRequired is returned before a local mutation when live
// propagation is requested without a durable outbox implementation.
var ErrDurableOutboxRequired = errors.New("durable outbox required for upstream mutations")
