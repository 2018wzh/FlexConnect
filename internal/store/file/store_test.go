package file

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"flexconnect/internal/types"
)

func TestPublicProfileJSONNeverContainsInternalOwnership(t *testing.T) {
	profile := types.Profile{ID: "p1", SecretRef: "profile/p1/secret", Scope: types.ProfileScopeUser, OwnerID: "S-1-5-21-test"}
	b, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret_ref") || strings.Contains(string(b), "owner_id") || strings.Contains(string(b), profile.OwnerID) {
		t.Fatalf("public profile leaked internal ownership: %s", b)
	}
}

func TestSaveIsAtomicPrivateAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := New(path)
	data := Data{SchemaVersion: CurrentSchemaVersion, ControlMode: "user", SelectedProfiles: map[string]string{}}
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".flexconnect-state-") {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}

func TestLoadRejectsCorruptUnknownAndTrailingData(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":  `{`,
		"unknown":  `{"schema_version":2,"control_mode":"user","unknown":true}`,
		"trailing": `{"schema_version":2,"control_mode":"user"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(path).Load(); err == nil {
				t.Fatal("invalid state was accepted")
			}
		})
	}
}

func TestStateRoundTripRetainsInternalOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	profile := types.Profile{ID: "p1", SecretRef: "profile/p1/secret", Scope: types.ProfileScopeUser, OwnerID: "S-1-5-21-test"}
	store := New(path)
	if err := store.Save(Data{SchemaVersion: CurrentSchemaVersion, Profiles: []types.Profile{profile}, ControlMode: "user", SelectedProfiles: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].OwnerID != profile.OwnerID || got.Profiles[0].SecretRef != profile.SecretRef {
		t.Fatalf("profile = %+v", got.Profiles)
	}
}
