package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.etcd.io/etcd/server/v3/embed"
)

// TestDetectClusterState_FreshStart tests detection when member directory doesn't exist
func TestDetectClusterState_FreshStart(t *testing.T) {
	tempDir := t.TempDir()

	state := detectClusterState(tempDir)

	if state != embed.ClusterStateFlagNew {
		t.Errorf("Expected cluster state to be %q, got %q", embed.ClusterStateFlagNew, state)
	}

	t.Logf("✓ Fresh directory correctly detected as 'new' cluster state")
}

// TestDetectClusterState_ExistingWithWAL tests detection when WAL files exist
func TestDetectClusterState_ExistingWithWAL(t *testing.T) {
	tempDir := t.TempDir()
	walDir := filepath.Join(tempDir, "member", "wal")

	if err := os.MkdirAll(walDir, 0755); err != nil {
		t.Fatalf("Failed to create WAL directory: %v", err)
	}

	// Create a WAL file
	walFile := filepath.Join(walDir, "0000000000000001-0000000000000001.wal")
	if err := os.WriteFile(walFile, []byte("wal data"), 0644); err != nil {
		t.Fatalf("Failed to create WAL file: %v", err)
	}

	state := detectClusterState(tempDir)

	if state != embed.ClusterStateFlagExisting {
		t.Errorf("Expected cluster state to be %q, got %q", embed.ClusterStateFlagExisting, state)
	}

	t.Logf("✓ WAL files correctly detected as 'existing' cluster state")
}

// TestDetectClusterState_ExistingWithSnapshotDB tests detection when snapshot DB exists
func TestDetectClusterState_ExistingWithSnapshotDB(t *testing.T) {
	tempDir := t.TempDir()
	snapDir := filepath.Join(tempDir, "member", "snap")
	snapDB := filepath.Join(snapDir, "db")

	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("Failed to create snap directory: %v", err)
	}

	// Create a non-empty snapshot DB file
	if err := os.WriteFile(snapDB, []byte("snapshot data"), 0644); err != nil {
		t.Fatalf("Failed to create snapshot DB: %v", err)
	}

	state := detectClusterState(tempDir)

	if state != embed.ClusterStateFlagExisting {
		t.Errorf("Expected cluster state to be %q, got %q", embed.ClusterStateFlagExisting, state)
	}

	t.Logf("✓ Snapshot DB correctly detected as 'existing' cluster state")
}

// TestDetectClusterState_ExistingWithBoth tests detection when both WAL and snapshot exist
func TestDetectClusterState_ExistingWithBoth(t *testing.T) {
	tempDir := t.TempDir()
	walDir := filepath.Join(tempDir, "member", "wal")
	snapDir := filepath.Join(tempDir, "member", "snap")
	snapDB := filepath.Join(snapDir, "db")

	if err := os.MkdirAll(walDir, 0755); err != nil {
		t.Fatalf("Failed to create WAL directory: %v", err)
	}
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("Failed to create snap directory: %v", err)
	}

	// Create both WAL and snapshot
	walFile := filepath.Join(walDir, "0000000000000001-0000000000000001.wal")
	if err := os.WriteFile(walFile, []byte("wal data"), 0644); err != nil {
		t.Fatalf("Failed to create WAL file: %v", err)
	}
	if err := os.WriteFile(snapDB, []byte("snapshot data"), 0644); err != nil {
		t.Fatalf("Failed to create snapshot DB: %v", err)
	}

	state := detectClusterState(tempDir)

	if state != embed.ClusterStateFlagExisting {
		t.Errorf("Expected cluster state to be %q, got %q", embed.ClusterStateFlagExisting, state)
	}

	t.Logf("✓ Both WAL and snapshot correctly detected as 'existing' cluster state")
}

// TestDetectClusterState_EmptyWALDirectory tests panic on empty WAL directory
func TestDetectClusterState_EmptyWALDirectory(t *testing.T) {
	tempDir := t.TempDir()
	memberDir := filepath.Join(tempDir, "member")
	walDir := filepath.Join(memberDir, "wal")

	if err := os.MkdirAll(walDir, 0755); err != nil {
		t.Fatalf("Failed to create WAL directory: %v", err)
	}

	// Should panic because member/ exists but no valid data
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for empty member directory, but didn't panic")
		} else {
			panicMsg := r.(string)
			if !strings.Contains(panicMsg, "no valid etcd data") {
				t.Errorf("Expected panic message about 'no valid etcd data', got: %v", panicMsg)
			}
			t.Logf("✓ Empty WAL directory correctly panics: %v", panicMsg)
		}
	}()

	detectClusterState(tempDir)
}

// TestDetectClusterState_EmptySnapshotDB tests that empty snapshot DB is ignored
func TestDetectClusterState_EmptySnapshotDB(t *testing.T) {
	tempDir := t.TempDir()
	memberDir := filepath.Join(tempDir, "member")
	snapDir := filepath.Join(memberDir, "snap")
	snapDB := filepath.Join(snapDir, "db")

	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("Failed to create snap directory: %v", err)
	}

	// Create an EMPTY snapshot DB (size 0)
	if err := os.WriteFile(snapDB, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty snapshot DB: %v", err)
	}

	// Should panic because member/ exists but no valid data (empty DB doesn't count)
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for empty snapshot DB, but didn't panic")
		} else {
			t.Logf("✓ Empty snapshot DB correctly panics: %v", r)
		}
	}()

	detectClusterState(tempDir)
}

// TestDetectClusterState_MemberIsFile tests panic when member/ is a file
func TestDetectClusterState_MemberIsFile(t *testing.T) {
	tempDir := t.TempDir()
	memberPath := filepath.Join(tempDir, "member")

	// Create 'member' as a file instead of directory
	if err := os.WriteFile(memberPath, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("Failed to create member file: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when member is a file, but didn't panic")
		} else {
			panicMsg := r.(string)
			if !strings.Contains(panicMsg, "not a directory") {
				t.Errorf("Expected panic about 'not a directory', got: %v", panicMsg)
			}
			t.Logf("✓ Member-is-file corruption correctly panics: %v", panicMsg)
		}
	}()

	detectClusterState(tempDir)
}

// TestDetectClusterState_PermissionDenied tests panic on permission errors
func TestDetectClusterState_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	tempDir := t.TempDir()
	memberDir := filepath.Join(tempDir, "member")

	if err := os.MkdirAll(memberDir, 0755); err != nil {
		t.Fatalf("Failed to create member directory: %v", err)
	}

	// Remove all permissions from member directory
	if err := os.Chmod(memberDir, 0000); err != nil {
		t.Fatalf("Failed to change permissions: %v", err)
	}
	defer os.Chmod(memberDir, 0755) // Restore for cleanup

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic on permission denied, but didn't panic")
		} else {
			t.Logf("✓ Permission denied correctly panics: %v", r)
		}
	}()

	detectClusterState(tempDir)
}

// TestDetectClusterState_Idempotency tests consistent behavior across calls
func TestDetectClusterState_Idempotency(t *testing.T) {
	tempDir := t.TempDir()

	// First call: should be "new"
	state1 := detectClusterState(tempDir)
	if state1 != embed.ClusterStateFlagNew {
		t.Errorf("First call: expected %q, got %q", embed.ClusterStateFlagNew, state1)
	}

	// Simulate etcd creating data structures
	walDir := filepath.Join(tempDir, "member", "wal")
	if err := os.MkdirAll(walDir, 0755); err != nil {
		t.Fatalf("Failed to create WAL directory: %v", err)
	}
	walFile := filepath.Join(walDir, "0000000000000001-0000000000000001.wal")
	if err := os.WriteFile(walFile, []byte("wal data"), 0644); err != nil {
		t.Fatalf("Failed to create WAL file: %v", err)
	}

	// Second call: should be "existing"
	state2 := detectClusterState(tempDir)
	if state2 != embed.ClusterStateFlagExisting {
		t.Errorf("Second call: expected %q, got %q", embed.ClusterStateFlagExisting, state2)
	}

	// Third call: should still be "existing"
	state3 := detectClusterState(tempDir)
	if state3 != embed.ClusterStateFlagExisting {
		t.Errorf("Third call: expected %q, got %q", embed.ClusterStateFlagExisting, state3)
	}

	t.Logf("✓ Function is idempotent: new → existing → existing")
}

// TestDetectClusterState_WALCompactedAwayButSnapshotExists tests the scenario
// where WAL files were compacted away but snapshot DB still exists
func TestDetectClusterState_WALCompactedAwayButSnapshotExists(t *testing.T) {
	tempDir := t.TempDir()
	memberDir := filepath.Join(tempDir, "member")
	walDir := filepath.Join(memberDir, "wal")
	snapDir := filepath.Join(memberDir, "snap")
	snapDB := filepath.Join(snapDir, "db")

	// Create empty WAL directory (compacted away)
	if err := os.MkdirAll(walDir, 0755); err != nil {
		t.Fatalf("Failed to create WAL directory: %v", err)
	}

	// Create snapshot DB with data
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("Failed to create snap directory: %v", err)
	}
	if err := os.WriteFile(snapDB, []byte("snapshot data"), 0644); err != nil {
		t.Fatalf("Failed to create snapshot DB: %v", err)
	}

	state := detectClusterState(tempDir)

	if state != embed.ClusterStateFlagExisting {
		t.Errorf("Expected cluster state to be %q, got %q", embed.ClusterStateFlagExisting, state)
	}

	t.Logf("✓ Compacted WAL with snapshot DB correctly detected as 'existing'")
}
