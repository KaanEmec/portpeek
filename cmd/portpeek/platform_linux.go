//go:build linux

package main

import "github.com/kaanemec/portpeek/internal/inspect/ss"

func defaultInspector() platformInspector { return ss.New() }
