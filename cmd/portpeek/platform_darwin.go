//go:build darwin

package main

import (
	"github.com/kaanemec/portpeek/internal/inspect"
	"github.com/kaanemec/portpeek/internal/inspect/lsof"
)

func defaultInspector() inspect.Inspector { return lsof.New() }
