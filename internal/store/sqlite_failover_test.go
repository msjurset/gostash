package store

import (
	"context"
	"testing"
	"time"
)

func TestFailoverApprovals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	op := "test-operation"

	// 1. Initially should not be approved
	approved, err := s.IsFailoverApproved(ctx, op)
	if err != nil {
		t.Fatalf("IsFailoverApproved: %v", err)
	}
	if approved {
		t.Error("expected approved to be false")
	}

	// 2. Approve with future expiry
	expiresAt := time.Now().Add(1 * time.Hour)
	err = s.ApproveFailover(ctx, op, expiresAt)
	if err != nil {
		t.Fatalf("ApproveFailover: %v", err)
	}

	approved, err = s.IsFailoverApproved(ctx, op)
	if err != nil {
		t.Fatalf("IsFailoverApproved: %v", err)
	}
	if !approved {
		t.Error("expected approved to be true")
	}

	// 3. Approve with past expiry
	expiresAt = time.Now().Add(-1 * time.Hour)
	err = s.ApproveFailover(ctx, op, expiresAt)
	if err != nil {
		t.Fatalf("ApproveFailover: %v", err)
	}

	approved, err = s.IsFailoverApproved(ctx, op)
	if err != nil {
		t.Fatalf("IsFailoverApproved: %v", err)
	}
	if approved {
		t.Error("expected approved to be false after expiry")
	}
}
