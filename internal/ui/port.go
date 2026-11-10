package ui

import (
	"context"
	"errors"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// Application is the inbound application boundary consumed by the Fyne adapter.
// Implementations own profile state and business rules; GUI owns widget state only.
type Application interface {
	Changes() <-chan application.Change
	Profiles() []profile.Profile
	Profile(string) (profile.Profile, bool)
	Status(string) application.Status
	Logs(string) []application.LogEntry
	RuntimeLocked(string) bool
	Add(string) (profile.Profile, error)
	Save(profile.Profile) (application.SaveResult, error)
	Delete(string) error
	Import(string, []byte) ([]profile.Profile, error)
	Export([]string) ([]byte, error)
	Connect(string) error
	Login(string) error
	ConnectAll() error
	AutoConnect() error
	Disconnect(string) bool
	DisconnectFromTray(string) bool
	DisconnectAll()
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
	Logout(context.Context, string) error
	Shutdown(context.Context) error
}

type profileFileDialog interface {
	OpenProfilePath() (string, error)
	SaveProfilesPath(fileName string) (string, error)
}

var errNativeFileDialogUnavailable = errors.New("native file picker is unavailable; install zenity, matedialog, or qarma on Unix-like systems")
