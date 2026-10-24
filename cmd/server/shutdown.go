package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type executionLifecycle interface {
	BeginDrain()
	CancelWork()
	WaitForWork(context.Context) error
}

func shutdownServer(server *http.Server, work executionLifecycle, grace time.Duration) error {
	work.BeginDrain()
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	err := server.Shutdown(ctx)
	cancel()
	work.CancelWork()
	if err != nil {
		err = errors.Join(err, server.Close())
	}
	// HTTP connection shutdown alone does not wait for canceled runner cleanup.
	// Do not exit until every admitted handler has finished its cleanup. A second
	// termination signal can still force an operator-requested immediate exit.
	return errors.Join(err, work.WaitForWork(context.Background()))
}
