package runthrough

// Write policy names reported in the startup banner and the server status
// payload. They describe what stow actually does, not what was requested.
const (
	// WritePolicyLocalOnly means no mutation ever leaves the local store.
	WritePolicyLocalOnly = "local-only"
	// WritePolicyMirrorWrites means supported mutations propagate upstream and
	// a mirrorWrites policy is also in effect.
	WritePolicyMirrorWrites = "mirrorWrites"
	// WritePolicyAllowLiveWrites means supported mutations propagate upstream
	// under the read-through policy.
	WritePolicyAllowLiveWrites = "allowLiveWrites"
	// WritePolicyMirrorWritesDisabled means a mirrorWrites policy is configured
	// but propagation is not permitted, so writes still stay local.
	WritePolicyMirrorWritesDisabled = "mirrorWrites-disabled"
)

// EffectiveWritePolicy collapses Policy and AllowLiveWrites into the one name
// that describes real behavior.
//
// Both are needed to propagate. A policy is a routing choice; the live-write
// flag is the consent. Reporting the policy alone once let stow claim it would
// propagate mutations when it would not, and was duplicated across the banner
// and the server status payload, so it drifted.
//
// Callers must use this instead of deriving the pair themselves.
func EffectiveWritePolicy(cfg Config) string {
	switch {
	case cfg.Policy == PolicyMirrorWrites && cfg.AllowLiveWrites:
		return WritePolicyMirrorWrites
	case cfg.Policy == PolicyMirrorWrites:
		return WritePolicyMirrorWritesDisabled
	case cfg.AllowLiveWrites:
		return WritePolicyAllowLiveWrites
	default:
		return WritePolicyLocalOnly
	}
}

// PropagatesUpstream reports whether a supported mutation would leave the local
// store under this configuration. Startup validation uses it to reject a
// combination that cannot work, and the banner uses it to stay truthful.
func PropagatesUpstream(cfg Config) bool {
	return cfg.AllowLiveWrites
}
