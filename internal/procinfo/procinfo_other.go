//go:build !linux && !darwin

package procinfo

var sys backend // nil: ErrUnsupported
