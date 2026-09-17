package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestCreateUserWithAuthElectsTheFirstUserAdmin(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-first", "alice", "hash", "totp"); err != nil {
		t.Fatalf("create first user: %v", err)
	}
	first, err := database.Identity.GetUserBySubject("alice")
	if err != nil {
		t.Fatalf("get first user: %v", err)
	}
	if first.Role != "admin" {
		t.Fatalf("first user role = %q, want admin", first.Role)
	}
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-second", "bob", "hash", "totp"); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	second, err := database.Identity.GetUserBySubject("bob")
	if err != nil {
		t.Fatalf("get second user: %v", err)
	}
	if second.Role != "user" {
		t.Fatalf("second user role = %q, want user", second.Role)
	}
}

func TestConcurrentFirstRegistrationElectsExactlyOneAdmin(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	const racers = 4
	roles := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, subject := fmt.Sprintf("u-race-%d", i), fmt.Sprintf("racer-%d", i)
			if _, err := database.Identity.CreateUserWithAuth(ctx, id, subject, "hash", "totp"); err != nil {
				errs[i] = err
				return
			}
			user, err := database.Identity.GetUserBySubject(subject)
			if err != nil {
				errs[i] = err
				return
			}
			roles[i] = user.Role
		}(i)
	}
	wg.Wait()
	admins := 0
	for i := range roles {
		if errs[i] != nil {
			t.Fatalf("racer %d failed: %v", i, errs[i])
		}
		switch roles[i] {
		case "admin":
			admins++
		case "user":
		default:
			t.Fatalf("racer %d role = %q, want user or admin", i, roles[i])
		}
	}
	if admins != 1 {
		t.Fatalf("concurrent first registration elected %d admins, want exactly one", admins)
	}
}

func TestListUsersExcludesCredentialColumns(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-list-1", "alice", "secret-hash", "secret-totp"); err != nil {
		t.Fatalf("create first user: %v", err)
	}
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-list-2", "bob", "secret-hash", "secret-totp"); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	users, err := database.Identity.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("listed %d users, want 2", len(users))
	}
	for _, user := range users {
		if user.ID == "" || user.Subject == "" || user.Name == "" || user.Role == "" || user.CreatedAt.IsZero() {
			t.Fatalf("user summary is incomplete: %#v", user)
		}
	}
	if users[0].ID != "u-list-1" || users[0].Role != "admin" {
		t.Fatalf("first summary = %#v, want oldest admin first", users[0])
	}
}

func TestUpdateTOTPSecretReplacesOnlyTheTargetUser(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-totp-1", "alice", "hash", "old-totp"); err != nil {
		t.Fatalf("create first user: %v", err)
	}
	if _, err := database.Identity.CreateUserWithAuth(ctx, "u-totp-2", "bob", "hash", "old-totp"); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if err := database.Identity.UpdateTOTPSecret(ctx, "u-totp-1", "new-totp"); err != nil {
		t.Fatalf("update totp secret: %v", err)
	}
	alice, err := database.Identity.GetUserByID("u-totp-1")
	if err != nil {
		t.Fatalf("get alice: %v", err)
	}
	if alice.TOTPSecret != "new-totp" {
		t.Fatalf("alice totp = %q, want new-totp", alice.TOTPSecret)
	}
	bob, err := database.Identity.GetUserByID("u-totp-2")
	if err != nil {
		t.Fatalf("get bob: %v", err)
	}
	if bob.TOTPSecret != "old-totp" {
		t.Fatalf("bob totp = %q, want old-totp", bob.TOTPSecret)
	}
	if err := database.Identity.UpdateTOTPSecret(ctx, "u-missing", "new-totp"); err == nil {
		t.Fatalf("update missing user totp = nil error, want failure")
	}
}
