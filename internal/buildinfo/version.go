package buildinfo

import "runtime/debug"

var (
	Version            = "dev"
	Commit             = "unknown"
	Date               = "unknown"
	TelemetryHost      = ""
	TelemetryWebsiteID = ""
)

func CurrentVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return "dev"
}

func String() string {
	return CurrentVersion() + " (" + Commit + ") " + Date
}
