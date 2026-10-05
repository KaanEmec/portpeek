//go:build darwin

package main

import "github.com/kaanemec/portpeek/internal/inspect/lsof"

func defaultInspector() platformInspector { return lsof.New() }
