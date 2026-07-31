package buildinfo

var Version = "1.1.0"

// UpdateRepo is the GitHub "owner/name" used for online update checks.
// It is overridable at build time via ldflags and at runtime via the
// FLEXCONNECT_UPDATE_REPO environment variable. An empty value disables
// update checks silently.
var UpdateRepo = ""

const LocalAPIVersion = "1"
