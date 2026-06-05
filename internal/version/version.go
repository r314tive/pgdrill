package version

var (
	Version = "0.0.0"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	return "pgdrill " + Version + " (" + Commit + ", " + Date + ")"
}
