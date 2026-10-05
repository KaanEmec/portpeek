//go:build !darwin && !linux && !windows

package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func defaultInspector() inspect.Inspector { return unsupported{} }

type unsupported struct{}

func (unsupported) Inspect(context.Context, inspect.Query) (inspect.Result, error) {
	return inspect.Result{}, &inspect.Error{
		Kind: inspect.KindUnsupported,
		Op:   "inspect",
		Err:  fmt.Errorf("%s is not supported yet", runtime.GOOS),
	}
}
