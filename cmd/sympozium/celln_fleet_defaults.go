package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// The one-liner: a build that pins a starter package turns a bare
// `sympozium install` into a fleet install. The package identity comes from
// the build and every node the probe finds KVM on joins by itself. Nothing
// about models is asked: the fleet calls no provider, and each Agent brings
// its own connection and key.

const defaultFleetScope = "starter"

func defaultFleetOutputDir(scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("--celln-fleet-output-dir is required when the home directory is unknown: %w", err)
	}
	return filepath.Join(home, ".sympozium", "celln-fleet", scope), nil
}

// applyStarterDefaults fills the package identity, scope and output directory
// from this build's pinned starter package when the flags omit them.
func (f *cellnFleetFlags) applyStarterDefaults() error {
	if f.options.PackageImage == "" && f.options.PackageHash == "" && f.options.Publisher == "" {
		d := defaultCellnStarter()
		if !d.complete() {
			return fmt.Errorf("--celln-fleet needs --celln-fleet-package-image, --celln-fleet-package-hash and --celln-fleet-publisher: this build carries no default starter package")
		}
		f.options.PackageImage, f.options.PackageHash, f.options.Publisher = d.Image, d.PackageHash, d.Publisher
		fmt.Printf("  Using the default Celln starter package (Celln %s) at %s\n", d.CellnVersion, d.Image)
	}
	if f.options.Scope == "" {
		f.options.Scope = defaultFleetScope
	}
	if f.outputDir == "" {
		dir, err := defaultFleetOutputDir(f.options.Scope)
		if err != nil {
			return err
		}
		f.outputDir = dir
	}
	return nil
}

// enableByDefault decides whether a bare `sympozium install` runs the fleet:
// whenever this build pins a starter package. It reports what it chose.
func (f *cellnFleetFlags) enableByDefault() bool {
	if !defaultCellnStarter().complete() {
		return false
	}
	f.defaulted = true
	return true
}
