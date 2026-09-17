package engine

import "testing"

// The mode decides whether tenants get their own files. Reading only the exact
// string "true" meant every other spelling silently chose the other mode.
func TestReadSchemaMode(t *testing.T) {
	on := []string{"true", "TRUE", "True", "1", "t", "T"}
	for _, v := range on {
		t.Setenv("OVERLITE_MULTITENANT_SCHEMA", v)
		readSchemaMode()
		if !schemaFilesMode {
			t.Errorf("OVERLITE_MULTITENANT_SCHEMA=%q did not enable multi-file schemas", v)
		}
	}

	off := []string{"false", "FALSE", "0", "f", ""}
	for _, v := range off {
		t.Setenv("OVERLITE_MULTITENANT_SCHEMA", v)
		readSchemaMode()
		if schemaFilesMode {
			t.Errorf("OVERLITE_MULTITENANT_SCHEMA=%q enabled multi-file schemas", v)
		}
	}

	// An unparseable value keeps the default rather than guessing.
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "yes-please")
	readSchemaMode()
	if schemaFilesMode {
		t.Error("an unparseable value enabled multi-file schemas")
	}
}
