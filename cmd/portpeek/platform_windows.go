//go:build windows

package main

import (
	"github.com/kaanemec/portpeek/internal/inspect"
	"github.com/kaanemec/portpeek/internal/inspect/netstat"
)

func defaultInspector() inspect.Inspector { return netstat.New() }
