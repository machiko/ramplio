package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// TestMain points the automatic history at a throwaway directory so no test
// in this package leaves a .ramplio/ behind in the source tree. Tests that
// inspect history set their own directory with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ramplio-history-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "建立測試用紀錄目錄失敗:", err)
		os.Exit(1)
	}
	if err := os.Setenv(historyDirEnv, dir); err != nil {
		fmt.Fprintln(os.Stderr, "設定測試用紀錄目錄失敗:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// execRun drives the real `run` command. Only output written through cobra's
// writers is captured; the long-standing summary still goes to os.Stdout.
func execRun(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRunCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}
