package cellninstall

import (
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Output tokens one model request of a backend may produce
// (modelConnection.maxOutputTokens of the starter-configure plan). A turn
// reserves api.TurnModelRequests requests of that size from its parent's
// lifetime totals.
const (
	DefaultModelMaxOutputTokens = api.DefaultRequestOutputTokens
	MinModelMaxOutputTokens     = api.MinRequestOutputTokens
	MaxModelMaxOutputTokens     = api.MaxRequestOutputTokens
	// ModelMaxOutputTokensMinCelln is the newest Celln release whose plans
	// refuse a modelConnection.maxOutputTokens field.
	ModelMaxOutputTokensMinCelln = "v0.5.23"
)

// ValidateModelMaxOutputTokens is the one rule for a backend's output cap,
// used by the installer, the chart values and the API: 0 means Celln's default
// (DefaultModelMaxOutputTokens), anything else must be within Celln's range.
func ValidateModelMaxOutputTokens(tokens int64) error {
	return api.ValidateRequestOutputTokens(tokens)
}

// NormalModelMaxOutputTokens folds the default into "unset", so a backend
// asking for exactly the default carries nothing: its values, its entry in
// FLEET_BACKENDS and its plan stay what they were before the field existed.
func NormalModelMaxOutputTokens(tokens int64) int64 {
	if tokens == DefaultModelMaxOutputTokens {
		return 0
	}
	return tokens
}

// EffectiveModelMaxOutputTokens is the cap Celln applies.
func EffectiveModelMaxOutputTokens(tokens int64) int64 {
	return api.EffectiveRequestOutputTokens(tokens)
}

// TurnOutputTokensFor is what one turn of a backend reserves from its parent's
// lifetime output tokens: api.TurnModelRequests requests of the backend's cap.
func TurnOutputTokensFor(maxOutputTokens int64) int64 {
	return api.TurnModelRequests * EffectiveModelMaxOutputTokens(maxOutputTokens)
}

// SessionDefaultsFor is SessionDefaults for one runtime profile: its own
// per-turn allowance (spec.native.turnModelRequests/turnOutputTokens, else the
// current starter package's) decides how many turns the ceilings pay for.
func SessionDefaultsFor(ceilings api.EnduringRunSpec, profile *api.CellnRuntimeProfile) *api.EnduringRunSpec {
	turnRequests, turnTokens := ProfileTurnAllowance(profile)
	return SessionDefaultsAt(ceilings, turnRequests, turnTokens)
}

// ProfileTurnAllowance is what one turn on the profile reserves; a profile
// that states none is held to the current starter package's allowance.
func ProfileTurnAllowance(profile *api.CellnRuntimeProfile) (int64, int64) {
	if profile != nil && profile.Spec.Native != nil && profile.Spec.Native.TurnModelRequests > 0 && profile.Spec.Native.TurnOutputTokens > 0 {
		return profile.Spec.Native.TurnModelRequests, profile.Spec.Native.TurnOutputTokens
	}
	return api.TurnModelRequests, api.TurnOutputTokens
}
