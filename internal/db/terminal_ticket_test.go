package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTerminalTicketIsBoundOneTimeAndExpires(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	ticket := TerminalTicket{
		TokenHash: "ticket-one", UserID: "user-one", EnvironmentUID: "environment-one",
		ChallengeID: "challenge-one", WindowName: "shell-1", ExpiresAt: now.Add(time.Minute),
	}
	if err := database.CreateTerminalTicket(ctx, ticket, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimTerminalTicket(ctx, ticket.TokenHash, ticket.ChallengeID, "shell-2", now.Add(time.Second)); !errors.Is(err, ErrTerminalTicketInvalid) {
		t.Fatalf("claim with another window = %v, want invalid ticket", err)
	}
	claimed, err := database.ClaimTerminalTicket(ctx, ticket.TokenHash, ticket.ChallengeID, ticket.WindowName, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.UserID != ticket.UserID || claimed.EnvironmentUID != ticket.EnvironmentUID {
		t.Fatalf("claimed ticket = %#v", claimed)
	}
	if _, err := database.ClaimTerminalTicket(ctx, ticket.TokenHash, ticket.ChallengeID, ticket.WindowName, now.Add(3*time.Second)); !errors.Is(err, ErrTerminalTicketInvalid) {
		t.Fatalf("replayed ticket = %v, want invalid ticket", err)
	}

	expired := TerminalTicket{
		TokenHash: "ticket-expired", UserID: "user-one", EnvironmentUID: "environment-one",
		ChallengeID: "challenge-one", WindowName: "shell-1", ExpiresAt: now.Add(time.Second),
	}
	if err := database.CreateTerminalTicket(ctx, expired, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimTerminalTicket(ctx, expired.TokenHash, expired.ChallengeID, expired.WindowName, now.Add(2*time.Second)); !errors.Is(err, ErrTerminalTicketInvalid) {
		t.Fatalf("expired ticket = %v, want invalid ticket", err)
	}
}
