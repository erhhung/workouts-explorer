package main

import (
	"context"
	"strings"
	"testing"
)

func TestProvisionerRequiresDedicatedCredential(t *testing.T) {
	t.Setenv("ROLE_PROVISIONING_DATABASE_URL", "")
	t.Setenv("MIGRATION_DATABASE_URL", "postgresql://migration.invalid/workouts")
	t.Setenv("API_DATABASE_URL", "postgresql://api.invalid/workouts")
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ROLE_PROVISIONING_DATABASE_URL") {
		t.Fatalf("provisioner used a normal runtime credential: %v", err)
	}
}
