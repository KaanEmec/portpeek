//go:build !darwin && !linux && !windows

package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func defaultInspector() platformInspector { return unsupported{} }

type unsupported struct{}

func (unsupported) Inspect(context.Context, inspect.Query) (inspect.Result, error) {
	return inspect.Result{}, unsupportedError("inspect")
}

func (unsupported) List(context.Context) (inspect.Snapshot, error) {
	return inspect.Snapshot{}, unsupportedError("list")
}

func unsupportedError(op string) error {
	return &inspect.Error{
		Kind: inspect.KindUnsupported,
		Op:   op,
		Err:  fmt.Errorf("%s is not supported yet", runtime.GOOS),
	}
}
