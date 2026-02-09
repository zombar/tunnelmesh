//go:build darwin

package tun

import "github.com/songgao/water"

// nolint:revive // cfg required by CreateTUN signature but not used on Darwin
func configurePlatformTUN(cfg *water.Config, name, address string) {
	// macOS uses utun devices - water will auto-assign a utun number
	// The Name field exists but setting a custom name is not supported
}
