package main

import "testing"

func TestResolveLocalCredentials(t *testing.T) {
	t.Setenv("STOW_LOCAL_ACCESS_KEY_ID", "env-access")
	t.Setenv("STOW_LOCAL_SECRET_ACCESS_KEY", "env-secret")

	tests := []struct {
		name       string
		accessKey  string
		secretKey  string
		wantAccess string
		wantSecret string
	}{
		{
			name:       "flags win",
			accessKey:  "flag-access",
			secretKey:  "flag-secret",
			wantAccess: "flag-access",
			wantSecret: "flag-secret",
		},
		{
			name:       "environment fallback",
			wantAccess: "env-access",
			wantSecret: "env-secret",
		},
		{
			name:       "whitespace falls back",
			accessKey:  "  ",
			secretKey:  "\t",
			wantAccess: "env-access",
			wantSecret: "env-secret",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accessKey, secretKey := resolveLocalCredentials(test.accessKey, test.secretKey)
			if accessKey != test.wantAccess || secretKey != test.wantSecret {
				t.Fatalf("credentials = %q/%q, want %q/%q", accessKey, secretKey, test.wantAccess, test.wantSecret)
			}
		})
	}
}
