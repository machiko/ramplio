package engine

import (
	"sync/atomic"
)

// dataSource distributes data file rows to VUs.
type dataSource struct {
	rows    []map[string]string
	mode    string
	counter atomic.Int64
}

func newDataSource(rows []map[string]string, mode string) *dataSource {
	return &dataSource{rows: rows, mode: mode}
}

func (ds *dataSource) next() map[string]string {
	if len(ds.rows) == 0 {
		return nil
	}
	if ds.mode == "random" {
		// Use counter as a cheap pseudo-random index (avoids math/rand lock contention).
		idx := ds.counter.Add(1) - 1
		return ds.rows[int(idx*2654435761)%len(ds.rows)]
	}
	idx := ds.counter.Add(1) - 1
	return ds.rows[int(idx)%len(ds.rows)]
}
