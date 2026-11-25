// Package history keeps an automatic record of every run so results can be
// compared later without the user having to remember --save-baseline.
//
// A record file is a superset of the baseline format: the baseline fields sit
// at the top level, so `ramplio compare a.json b.json` and baseline.Load read
// a record directly (encoding/json ignores the extra fields).
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/reporter"
)

const (
	// DefaultKeep is how many records (all targets combined) are kept.
	DefaultKeep = 50
	// RecordsSubdir holds the record files inside the history directory.
	RecordsSubdir = "runs"

	gitignoreName    = ".gitignore"
	gitignoreContent = "*\n"
	recordExt        = ".json"
	// timestampLayout sorts lexically in chronological order (UTC, fixed width).
	timestampLayout = "20060102T150405.000Z"
	// maxNameAttempts bounds the collision retries for records saved in the same millisecond.
	maxNameAttempts = 100
)

// nowFunc is the clock; tests replace it to simulate the clock going backwards.
var nowFunc = time.Now

// Kind tells which command produced a record.
type Kind string

const (
	KindRun      Kind = "run"
	KindDiscover Kind = "discover"
)

// Record is one saved run. Report is set for run mode only (discover has no
// metrics.Summary to build a report from).
type Record struct {
	baseline.Baseline
	Kind Kind `json:"kind"`
	// TargetKey identifies "the same target with the same load settings";
	// only records with an equal key are compared automatically.
	TargetKey string           `json:"target_key"`
	Report    *reporter.Report `json:"report,omitempty"`
}

// Entry is a record read back from disk.
type Entry struct {
	Path   string
	Record Record
}

// SaveResult describes a successful save.
type SaveResult struct {
	Path string
	// WroteGitignore is true when this save wrote dir/.gitignore (it did not exist yet).
	WroteGitignore bool
	// NeedsIgnoreHint is true when records were put into a directory that holds
	// other things and has no .gitignore: writing `*` there could hide a whole
	// project from git, so the caller asks the user to ignore runs/ instead.
	// Only set on the save that first created runs/.
	NeedsIgnoreHint bool
	// PruneErr reports a failure to delete old records; the save itself succeeded.
	PruneErr error
}

// Save writes rec into dir/runs, creating dir when needed and adding a
// self-ignoring .gitignore if there is none, then deletes the oldest records
// beyond keep.
func Save(dir string, rec Record, keep int) (SaveResult, error) {
	if rec.Metrics == nil && rec.Discover == nil {
		return SaveResult{}, errors.New("執行紀錄無效: Metrics 與 Discover 不可同時為 nil")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return SaveResult{}, fmt.Errorf("序列化執行紀錄失敗: %w", err)
	}

	setup, err := ensureDir(dir)
	if err != nil {
		return SaveResult{}, err
	}
	path, err := writeRecord(filepath.Join(dir, RecordsSubdir), append(data, '\n'))
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{
		Path:            path,
		WroteGitignore:  setup.wroteGitignore,
		NeedsIgnoreHint: setup.needsIgnoreHint,
		PruneErr:        prune(filepath.Join(dir, RecordsSubdir), keep),
	}, nil
}

// ensureDir creates dir and dir/runs. It writes a .gitignore containing `*`
// only when dir is ours alone: created by this call, or holding nothing but
// runs/ (a user's empty mkdir counts). If dir also holds other things (for
// example RAMPLIO_HISTORY_DIR points at a project root or $HOME), `*` would
// hide all of them from git, so nothing is written and the caller is told to
// hint instead — only on the save that first creates runs/. An existing
// .gitignore is never touched.
func ensureDir(dir string) (dirSetup, error) {
	dirIsNew := !exists(dir)
	runsIsNew := !exists(filepath.Join(dir, RecordsSubdir))
	if err := os.MkdirAll(filepath.Join(dir, RecordsSubdir), 0o755); err != nil {
		return dirSetup{}, fmt.Errorf("建立紀錄目錄 %s 失敗: %w", dir, err)
	}
	path := filepath.Join(dir, gitignoreName)
	if exists(path) {
		return dirSetup{}, nil
	}
	if !dirIsNew && !holdsOnlyRecords(dir) {
		return dirSetup{needsIgnoreHint: runsIsNew}, nil
	}
	wrote, err := writeGitignore(path)
	return dirSetup{wroteGitignore: wrote}, err
}

// dirSetup is what ensureDir did about git ignoring.
type dirSetup struct {
	wroteGitignore  bool
	needsIgnoreHint bool
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// dirScanBatch bounds each directory read so a huge directory ($HOME) is not
// listed in full: one foreign entry is enough to decide.
const dirScanBatch = 16

// holdsOnlyRecords reports whether dir contains nothing but runs/ (and
// .gitignore). Any read error counts as "not only records" (fail safe: do not write).
func holdsOnlyRecords(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // 唯讀
	for {
		names, err := f.Readdirnames(dirScanBatch)
		for _, name := range names {
			if name != RecordsSubdir && name != gitignoreName {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// writeGitignore creates path with `*` unless it already exists (O_EXCL).
func writeGitignore(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("寫入 %s 失敗: %w", path, err)
	}
	_, writeErr := f.WriteString(gitignoreContent)
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return true, fmt.Errorf("寫入 %s 失敗: %w", path, writeErr)
	}
	return true, nil
}

// writeRecord reserves a unique file name with O_EXCL, then replaces it
// atomically with the full content (temp file + rename), so a crash never
// leaves a half-written record under a final name. The name is only time and
// sequence, so name order is save order even within one millisecond (the
// kind lives in the content).
func writeRecord(runsDir string, data []byte) (string, error) {
	stamp := nextStamp(runsDir)
	for seq := 0; seq < maxNameAttempts; seq++ {
		path := filepath.Join(runsDir, fmt.Sprintf("%s-%02d%s", stamp, seq, recordExt))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("建立執行紀錄 %s 失敗: %w", path, err)
		}
		_ = f.Close() // empty placeholder; replaced by the rename below

		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("寫入暫存檔 %s 失敗: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			_ = os.Remove(path)
			return "", fmt.Errorf("寫入執行紀錄 %s 失敗: %w", path, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("同一毫秒內的執行紀錄超過 %d 筆，放棄存檔", maxNameAttempts)
}

// nextStamp is the current time, or 1ms after the newest existing record if
// that is later (the clock went backwards, or many saves in one millisecond),
// so a new record always sorts after the existing ones.
func nextStamp(runsDir string) string {
	stamp := nowFunc().UTC()
	names, _ := recordNames(runsDir) // unreadable dir: writing fails right after anyway
	// Scan every name: a foreign file that sorts last must not hide the
	// newest real timestamp.
	for _, name := range names {
		if latest, ok := stampOf(name); ok && !latest.Before(stamp) {
			stamp = latest.Add(time.Millisecond)
		}
	}
	return stamp.Format(timestampLayout)
}

// stampOf parses the timestamp prefix of a record file name.
func stampOf(name string) (time.Time, bool) {
	if len(name) < len(timestampLayout) {
		return time.Time{}, false
	}
	t, err := time.Parse(timestampLayout, name[:len(timestampLayout)])
	return t, err == nil
}

// recordNames lists record file names in runsDir, oldest first.
func recordNames(runsDir string) ([]string, error) {
	items, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, it := range items {
		if it.Type().IsRegular() && strings.HasSuffix(it.Name(), recordExt) {
			names = append(names, it.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func prune(runsDir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	names, err := recordNames(runsDir)
	if err != nil {
		return fmt.Errorf("列出執行紀錄失敗: %w", err)
	}
	return removeOldest(runsDir, names, keep)
}

// removeOldest deletes all but the last keep of names (oldest first). A file
// that is already gone (another process pruned it) is not a failure.
func removeOldest(runsDir string, names []string, keep int) error {
	var errs []error
	for _, name := range names[:max(0, len(names)-keep)] {
		err := os.Remove(filepath.Join(runsDir, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("刪除舊紀錄 %s 失敗: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// List reads every record under dir, newest first. A missing directory is
// simply empty. Invalid files are skipped and reported in skipped; files that
// are being written (0 bytes) or vanished meanwhile are skipped silently.
// err is set when the directory itself cannot be read (a file, no permission).
func List(dir string) (entries []Entry, skipped []error, err error) {
	runsDir := filepath.Join(dir, RecordsSubdir)
	names, err := recordNames(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("讀取紀錄目錄 %s 失敗: %w", dir, err)
	}

	for i := len(names) - 1; i >= 0; i-- {
		path := filepath.Join(runsDir, names[i])
		rec, loadErr := load(path)
		if errors.Is(loadErr, errInProgress) || errors.Is(loadErr, fs.ErrNotExist) {
			continue
		}
		if loadErr != nil {
			skipped = append(skipped, loadErr)
			continue
		}
		entries = append(entries, Entry{Path: path, Record: rec})
	}
	return entries, skipped, nil
}

// errInProgress marks an empty file: the O_EXCL placeholder of a record that
// another process is still writing.
var errInProgress = errors.New("執行紀錄寫入中")

// load validates through baseline.Parse (same invariants and schema check as
// compare) before decoding the record-only fields.
func load(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("讀取執行紀錄 %s 失敗: %w", filepath.Base(path), err)
	}
	if len(raw) == 0 {
		return Record{}, errInProgress
	}
	if _, err := baseline.Parse(raw); err != nil {
		return Record{}, fmt.Errorf("執行紀錄 %s 無效: %w", filepath.Base(path), err)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("執行紀錄 %s 無效: %w", filepath.Base(path), err)
	}
	return rec, nil
}

// PreviousSameTarget returns the newest entry (entries are newest first) with
// the given target key, skipping excludePath.
func PreviousSameTarget(entries []Entry, key, excludePath string) (Entry, bool) {
	if key == "" {
		return Entry{}, false
	}
	for _, e := range entries {
		if e.Path != excludePath && e.Record.TargetKey == key {
			return e, true
		}
	}
	return Entry{}, false
}

// LatestWithReport returns the newest entry that carries a run report.
func LatestWithReport(entries []Entry) (Entry, bool) {
	for _, e := range entries {
		if e.Record.Report != nil {
			return e, true
		}
	}
	return Entry{}, false
}

// fingerprintLen is how many hex characters of the sha256 are kept.
const fingerprintLen = 8

// RequestFingerprint summarises headers and body as the first 8 hex chars of
// a sha256, so "same target" can include them without storing secrets.
// Header names are case-insensitive and order does not matter; values and
// body are compared exactly. Every part is length-prefixed so the boundary
// between a header value and the body cannot be shifted.
func RequestFingerprint(headers map[string]string, body []byte) string {
	type header struct{ lower, orig, value string }
	list := make([]header, 0, len(headers))
	for name, value := range headers {
		list = append(list, header{strings.ToLower(name), name, value})
	}
	// Sort by the normalised name first (order and case do not matter), then
	// by the original name and value so names that differ only in case
	// (`X-A` and `x-a` both given) still hash the same every time.
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.lower != b.lower {
			return a.lower < b.lower
		}
		if a.orig != b.orig {
			return a.orig < b.orig
		}
		return a.value < b.value
	})

	h := sha256.New()
	for _, hd := range list {
		writePart(h, hd.lower)
		writePart(h, hd.value)
	}
	writePart(h, string(body))
	return hex.EncodeToString(h.Sum(nil))[:fingerprintLen]
}

func writePart(w io.Writer, part string) {
	_, _ = fmt.Fprintf(w, "%d:%s;", len(part), part) // hash.Hash writes never fail
}
