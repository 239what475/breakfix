package build

var (
	Version   = "dev"
	BuildTime = "unknown"
	Commit    = "unknown"
	Mode      = "dev" // dev | prod
)

func IsDev() bool  { return Mode == "dev" }
func IsProd() bool { return Mode == "prod" }
