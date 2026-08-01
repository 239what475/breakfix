package buildinfo

// Set via ldflags at compile time.
var (
	Version   = "dev"
	BuildTime = "unknown"
	Commit    = "unknown"
)
