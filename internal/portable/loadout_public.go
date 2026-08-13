package portable

// ValidateLoadout validates the canonical, content-addressed loadout envelope.
// It does not assert that historical references are still current; callers
// that intend to deliver a loadout must use LoadCurrentLoadout.
func ValidateLoadout(loadout Loadout) error {
	return validateLoadoutEnvelope(loadout)
}
