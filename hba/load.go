package hba

import (
	"os"
	"path/filepath"
)

// Load reads the HBA policy from dir. If pg_hba.conf exists it wins; otherwise
// pg_hba.yaml (or pg_hba.yml) is used. When none of them exist it returns
// (nil, nil), meaning "no HBA configured" — the caller then falls back to its
// default single-method authentication.
func Load(dir string) (*Policy, error) {
	if f, err := os.Open(filepath.Join(dir, "pg_hba.conf")); err == nil {
		defer f.Close()
		return ConfParser{}.Parse(f)
	}
	for _, name := range []string{"pg_hba.yaml", "pg_hba.yml"} {
		if f, err := os.Open(filepath.Join(dir, name)); err == nil {
			defer f.Close()
			return YAMLParser{}.Parse(f)
		}
	}
	return nil, nil
}
