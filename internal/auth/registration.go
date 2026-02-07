package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Registration holds per-mesh user registration data.
// This is stored in ~/.tunnelmesh/contexts/{name}/registration.json
type Registration struct {
	UserID       string    `json:"user_id"`
	MeshDomain   string    `json:"mesh_domain"`
	RegisteredAt time.Time `json:"registered_at"`
	Certificate  string    `json:"certificate,omitempty"` // PEM-encoded user certificate
	Roles        []string  `json:"roles"`                 // Assigned roles on this mesh
	S3AccessKey  string    `json:"s3_access_key,omitempty"`
	S3SecretKey  string    `json:"s3_secret_key,omitempty"`
}

// NewRegistration creates a new Registration with the given user ID and mesh domain.
func NewRegistration(userID, meshDomain string) *Registration {
	return &Registration{
		UserID:       userID,
		MeshDomain:   meshDomain,
		RegisteredAt: time.Now().UTC(),
		Roles:        []string{},
	}
}

// Save writes the registration to a file with restricted permissions.
func (r *Registration) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// LoadRegistration reads a registration from a file.
func LoadRegistration(path string) (*Registration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var reg Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, err
	}

	return &reg, nil
}

// HasRole checks if the user has a specific role on this mesh.
func (r *Registration) HasRole(role string) bool {
	for _, r := range r.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsAdmin returns true if the user has the admin role.
func (r *Registration) IsAdmin() bool {
	return r.HasRole("admin")
}

// DefaultRegistrationPath returns the default path for a context's registration file.
func DefaultRegistrationPath(contextName string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".tunnelmesh", "contexts", contextName, "registration.json")
}
