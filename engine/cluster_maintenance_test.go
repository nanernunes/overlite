package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"overlite/engine"
)

// The maintenance database has to be there whatever the server was pointed at.
// It was added once and then lost when the directory started being taken from
// the file, because nothing asserted it — this is that assertion.
func TestMaintenanceDatabaseAlwaysExists(t *testing.T) {
	dir := t.TempDir()

	cluster, err := engine.OpenCluster(filepath.Join(dir, "shop.db"), 4)
	if err != nil {
		t.Fatalf("OpenCluster: %v", err)
	}
	defer cluster.Close()

	names, err := cluster.Databases()
	if err != nil {
		t.Fatalf("Databases: %v", err)
	}
	if !slices.Contains(names, engine.MaintenanceDatabase) {
		t.Fatalf("databases = %v, want %q among them", names, engine.MaintenanceDatabase)
	}
	if !slices.Contains(names, "shop") {
		t.Errorf("databases = %v, want the entry database too", names)
	}

	// It has to be connectable, not merely listed: the point is that a client
	// with nowhere else to go can land on it and run CREATE DATABASE.
	if _, err := cluster.Engine(context.Background(), engine.MaintenanceDatabase); err != nil {
		t.Fatalf("Engine(%q): %v", engine.MaintenanceDatabase, err)
	}
	cluster.Release(engine.MaintenanceDatabase)
}

// Pointing overlite at the maintenance database itself must not trip over
// creating it twice.
func TestMaintenanceDatabaseAsTheEntry(t *testing.T) {
	dir := t.TempDir()

	cluster, err := engine.OpenCluster(filepath.Join(dir, "postgres.db"), 4)
	if err != nil {
		t.Fatalf("OpenCluster: %v", err)
	}
	defer cluster.Close()

	names, err := cluster.Databases()
	if err != nil {
		t.Fatalf("Databases: %v", err)
	}
	if len(names) != 1 || names[0] != engine.MaintenanceDatabase {
		t.Fatalf("databases = %v, want exactly %q", names, engine.MaintenanceDatabase)
	}
}

// An existing directory gains the maintenance database without disturbing what
// is already there — which is what an upgrade looks like.
func TestMaintenanceDatabaseIsAddedToAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"acme", "globex"} {
		if err := os.WriteFile(filepath.Join(dir, name+".db"), nil, 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	cluster, err := engine.OpenCluster(filepath.Join(dir, "acme.db"), 4)
	if err != nil {
		t.Fatalf("OpenCluster: %v", err)
	}
	defer cluster.Close()

	names, err := cluster.Databases()
	if err != nil {
		t.Fatalf("Databases: %v", err)
	}
	for _, want := range []string{"acme", "globex", engine.MaintenanceDatabase} {
		if !slices.Contains(names, want) {
			t.Errorf("databases = %v, want %q among them", names, want)
		}
	}
}

// Dropping it would put the server back in the state this guarantee exists to
// prevent.
func TestMaintenanceDatabaseCannotBeDropped(t *testing.T) {
	dir := t.TempDir()
	cluster, err := engine.OpenCluster(filepath.Join(dir, "shop.db"), 4)
	if err != nil {
		t.Fatalf("OpenCluster: %v", err)
	}
	defer cluster.Close()

	if err := cluster.DropDatabase(context.Background(), engine.MaintenanceDatabase); err == nil {
		t.Fatal("DropDatabase on the maintenance database returned nil")
	}
}
