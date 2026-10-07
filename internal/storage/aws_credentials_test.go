package storage_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sendrec/sendrec/internal/storage"
)

func TestCredentialModesSignUploadsAndDownloads(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary-default-chain", true: "explicit-local"}[explicit], func(t *testing.T) {
			t.Setenv("AWS_ACCESS_KEY_ID", "synthetic-role-key")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "synthetic-role-secret")
			t.Setenv("AWS_SESSION_TOKEN", "synthetic-session-token")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			cfg := storage.Config{Endpoint: "https://storage.example.test", Bucket: "synthetic", Region: "ap-southeast-2"}
			wantKey, wantToken := "synthetic-role-key", "synthetic-session-token"
			if explicit {
				cfg.AccessKey, cfg.SecretKey = "synthetic-local-key", "synthetic-local-secret"
				wantKey, wantToken = cfg.AccessKey, ""
			}
			store, err := storage.New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			upload, err := store.GenerateUploadURL(context.Background(), "fixture.webm", "video/webm", 32, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			download, err := store.GenerateDownloadURL(context.Background(), "fixture.webm", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for _, signed := range []string{upload, download} {
				u, err := url.Parse(signed)
				if err != nil {
					t.Fatal(err)
				}
				q := u.Query()
				if !strings.HasPrefix(q.Get("X-Amz-Credential"), wantKey+"/") || q.Get("X-Amz-Security-Token") != wantToken || q.Get("X-Amz-Signature") == "" {
					t.Fatal("request did not use the expected credential mode")
				}
			}
		})
	}
}

func TestPartialExplicitCredentialsRejected(t *testing.T) {
	for _, cfg := range []storage.Config{{AccessKey: "synthetic-key"}, {SecretKey: "synthetic-secret"}} {
		if _, err := storage.New(context.Background(), cfg); err == nil {
			t.Fatal("partial credentials were accepted")
		}
	}
}
