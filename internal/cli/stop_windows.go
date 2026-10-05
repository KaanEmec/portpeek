//go:build windows

package cli

import "errors"

// errStopUnsupported is returned until a Windows stop mechanism exists.
var errStopUnsupported = errors.New("stopping processes is unsupported on windows")

// terminate is not implemented on Windows.
func terminate(int) error { return errStopUnsupported }

// alive is not implemented on Windows; it is only consulted after terminate
// succeeds, which it never does.
func alive(int) bool { return false }
