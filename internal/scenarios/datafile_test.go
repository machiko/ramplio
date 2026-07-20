package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseCSVRows shares the exact contract loadCSV relies on, so in-memory data
// (e.g. a generated CSV loaded into the dashboard) matches a disk load row-for-row.
func TestParseCSVRows_Basic(t *testing.T) {
	rows, err := ParseCSVRows(strings.NewReader("id,email\n1,a@b.com\n2,c@d.com\n"))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "1", rows[0]["id"])
	assert.Equal(t, "c@d.com", rows[1]["email"])
}

func TestParseCSVRows_HeaderOnlyIsError(t *testing.T) {
	_, err := ParseCSVRows(strings.NewReader("id,email\n"))
	assert.Error(t, err)
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoadCSV_Basic(t *testing.T) {
	path := writeTemp(t, "users.csv", "email,password\nuser1@example.com,pass1\nuser2@example.com,pass2\n")
	rows, err := LoadDataFile(path)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "user1@example.com", rows[0]["email"])
	assert.Equal(t, "pass1", rows[0]["password"])
	assert.Equal(t, "user2@example.com", rows[1]["email"])
}

func TestLoadCSV_EmptyError(t *testing.T) {
	path := writeTemp(t, "empty.csv", "email,password\n")
	_, err := LoadDataFile(path)
	assert.Error(t, err)
}

func TestLoadJSON_Basic(t *testing.T) {
	path := writeTemp(t, "users.json", `[{"email":"a@b.com","role":"admin"},{"email":"c@d.com","role":"user"}]`)
	rows, err := LoadDataFile(path)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "a@b.com", rows[0]["email"])
	assert.Equal(t, "admin", rows[0]["role"])
}

func TestLoadJSON_EmptyError(t *testing.T) {
	path := writeTemp(t, "empty.json", "[]")
	_, err := LoadDataFile(path)
	assert.Error(t, err)
}

func TestLoadDataFile_UnsupportedExt(t *testing.T) {
	path := writeTemp(t, "data.txt", "foo,bar")
	_, err := LoadDataFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestLoadCSV_ShortRow(t *testing.T) {
	// Row with fewer columns than headers — should not panic
	path := writeTemp(t, "short.csv", "a,b,c\n1,2\n")
	rows, err := LoadDataFile(path)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "1", rows[0]["a"])
	assert.Equal(t, "2", rows[0]["b"])
	assert.Equal(t, "", rows[0]["c"])
}
