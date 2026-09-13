package config

import "testing"

func TestRequireHTTPSExceptLoopback(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{"https real host", "https://keycloak.example.com", false},
		{"http localhost", "http://localhost:8180", false},
		{"http 127.0.0.1", "http://127.0.0.1:8180", false},
		{"http ::1", "http://[::1]:8180", false},
		{"http real host rejected", "http://keycloak.example.com", true},
		{"http real host with path rejected", "http://internal-keycloak/realms/x", true},
		{"invalid URL rejected", "://not-a-url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireHTTPSExceptLoopback(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("requireHTTPSExceptLoopback(%q) error = %v, wantErr %v", tt.rawURL, err, tt.wantErr)
			}
		})
	}
}
