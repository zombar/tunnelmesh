package coord

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShares_List_NoManager(t *testing.T) {
	srv := newTestServer(t)

	// Temporarily nil out fileShareMgr to test the guard
	orig := srv.fileShareMgr
	srv.fileShareMgr = nil
	defer func() { srv.fileShareMgr = orig }()

	req := httptest.NewRequest(http.MethodGet, "/api/shares", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "file shares not enabled")
}

func TestValidateShareName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "myshare", false},
		{"valid with numbers", "share123", false},
		{"valid with underscore", "my_share", false},
		{"valid with hyphen", "my-share", false},
		{"empty", "", false}, // empty is valid (caught by caller)
		{"too long", "a234567890123456789012345678901234567890123456789012345678901234", true},
		{"hyphen at start", "-myshare", true},
		{"hyphen at end", "myshare-", true},
		{"space", "my share", true},
		{"dot", "my.share", true},
		{"slash", "my/share", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateShareName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateShareQuota(t *testing.T) {
	tests := []struct {
		name    string
		input   int64
		wantErr bool
	}{
		{"zero (unlimited)", 0, false},
		{"valid 1GB", 1024 * 1024 * 1024, false},
		{"valid 1TB (max)", 1024 * 1024 * 1024 * 1024, false},
		{"negative", -1, true},
		{"exceeds max", 1024*1024*1024*1024 + 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateShareQuota(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
