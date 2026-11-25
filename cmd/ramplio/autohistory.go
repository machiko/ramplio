package main

import "os"

const (
	// historyDirEnv overrides where run records are kept (CI, tests).
	historyDirEnv = "RAMPLIO_HISTORY_DIR"
	// defaultHistoryDir lives in the working directory so records follow the project.
	defaultHistoryDir = ".ramplio"
)

// historyDir is where automatic run records are kept.
func historyDir() string {
	if dir := os.Getenv(historyDirEnv); dir != "" {
		return dir
	}
	return defaultHistoryDir
}
