//go:build windows

package main

import "github.com/kaanemec/portpeek/internal/inspect/netstat"

func defaultInspector() platformInspector { return netstat.New() }
