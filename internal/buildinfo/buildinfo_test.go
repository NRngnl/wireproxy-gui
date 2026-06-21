package buildinfo

import "testing"

func TestSummaryIncludesReleaseMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	defer func() {
		Version, Commit, Date = oldVersion, oldCommit, oldDate
	}()

	Version = "0.1.0"
	Commit = "abcdef123456"
	Date = "2026-06-21T10:00:00Z"

	got := Summary()
	want := "Wireproxy GUI 0.1.0 (abcdef123456) 2026-06-21T10:00:00Z"
	if got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}

func TestWindowTitleOmitsDevelopmentVersion(t *testing.T) {
	oldVersion := Version
	defer func() {
		Version = oldVersion
	}()

	Version = "dev"

	got := WindowTitle()
	if got != AppName {
		t.Fatalf("WindowTitle() = %q, want %q", got, AppName)
	}
}
