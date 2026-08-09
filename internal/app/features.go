package app

import "errors"

// enableQuarkProvider is intentionally a source-level safety gate. PanFind
// does not expose a flag, environment variable, config file, or build tag that
// can enable the experimental Quark provider. Anyone choosing to use it must
// review the implementation, change this constant, and rebuild PanFind.
const enableQuarkProvider = false

func quarkProviderDisabledError() error {
	return errors.New("Quark provider is disabled in source; review the implementation, set enableQuarkProvider to true in internal/app/features.go, and rebuild PanFind")
}
