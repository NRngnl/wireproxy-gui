package buildinfo

import "strings"

const AppName = "Wireproxy GUI"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func Summary() string {
	parts := []string{AppName, Version}
	if Commit != "" && Commit != "unknown" {
		parts = append(parts, "("+Commit+")")
	}
	if Date != "" && Date != "unknown" {
		parts = append(parts, Date)
	}
	return strings.Join(parts, " ")
}

func WindowTitle() string {
	if Version == "" || Version == "dev" {
		return AppName
	}
	return AppName + " " + Version
}
