package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigStoreProtectsCredentials(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
	}{
		{name: "new file"},
		{name: "existing file", existing: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if tt.existing {
				if err := os.WriteFile(path, []byte("stale: true\n"), 0644); err != nil {
					t.Fatalf("create existing config: %v", err)
				}
			}

			want := &config{
				App: appConfig{ID: "app-id", APIKey: "app-key"},
				AppInstance: appInstanceConfig{
					ID:     "instance-id",
					APIKey: "instance-key",
					Tenant: tenantConfig{ID: "tenant-id", Token: "tenant-token"},
				},
			}
			if err := want.store(path); err != nil {
				t.Fatalf("store config: %v", err)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat config: %v", err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("config permissions = %04o, want 0600", info.Mode().Perm())
			}

			got, err := loadConfig(path)
			if err != nil {
				t.Fatalf("load stored config: %v", err)
			}
			if got.App.APIKey != want.App.APIKey || got.AppInstance.APIKey != want.AppInstance.APIKey || got.AppInstance.Tenant.Token != want.AppInstance.Tenant.Token {
				t.Fatalf("stored credentials do not match input")
			}
		})
	}
}
