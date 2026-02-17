package coord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateS3Name(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid name", "my-bucket", false},
		{"empty", "", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"contains dotdot", "foo/../bar", true},
		{"absolute path", "/etc/passwd", true},
		{"backslash prefix", "\\etc\\passwd", true},
		{"valid nested", "folder/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateS3Name(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
