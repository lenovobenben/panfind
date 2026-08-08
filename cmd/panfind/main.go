package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/lenovobenben/panfind/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(app.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
