package collection

import (
	"context"
	"errors"
	"testing"
)

func TestCaptureWaitCancellation(t *testing.T) {
	var reads captureReads
	release, err := reads.acquire(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = reads.acquire(ctx, "synthetic"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled duplicate blocked")
	}
}
