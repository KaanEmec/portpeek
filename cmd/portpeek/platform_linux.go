//go:build linux

package main

import (
	"github.com/kaanemec/portpeek/internal/inspect"
	"github.com/kaanemec/portpeek/internal/inspect/ss"
)

func defaultInspector() inspect.Inspector { return ss.New() }
