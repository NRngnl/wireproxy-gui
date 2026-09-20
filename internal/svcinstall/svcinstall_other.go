//go:build !linux && !darwin

// This file provides the fallback Installer for every platform without a
// dedicated backend file (v1: everything except linux and darwin).
package svcinstall

import "context"

// unsupportedInstaller is the fallback Installer for every platform
// without a real backend (v1: everything except linux and darwin,
// including windows until Task 10 lands).
type unsupportedInstaller struct{}

func newPlatformInstaller() Installer {
	return unsupportedInstaller{}
}

func (unsupportedInstaller) Supported() bool { return false }

func (unsupportedInstaller) Status(context.Context) (Status, error) {
	return Status{Detail: ErrUnsupported.Error()}, ErrUnsupported
}

func (unsupportedInstaller) Install(context.Context, Options) error {
	return ErrUnsupported
}

func (unsupportedInstaller) Uninstall(context.Context) error {
	return ErrUnsupported
}
