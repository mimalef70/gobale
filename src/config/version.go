package config

import "regexp"

var releaseVersion = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(alpha|beta|rc)\.[1-9][0-9]*)?$`)

// ReleaseStage describes artifact versioning, not provider capability coverage.
func ReleaseStage(version string) string {
	match := releaseVersion.FindStringSubmatch(version)
	if match == nil {
		return "development"
	}
	if match[1] != "" {
		return match[1]
	}
	return "release"
}
