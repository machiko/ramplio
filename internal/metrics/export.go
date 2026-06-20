package metrics

import (
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// HistEnvelope is a JSON-serializable HDR histogram snapshot. It carries the
// raw bucket counts so a histogram can be reconstructed and merged losslessly
// on another machine — unlike pre-computed percentiles, which cannot be merged
// correctly (the p99 of merged data is not the average of per-shard p99s).
type HistEnvelope struct {
	Lo     int64   `json:"lo"`
	Hi     int64   `json:"hi"`
	Sig    int64   `json:"sig"`
	Counts []int64 `json:"counts"`
}

// StepExport carries one named step's counters plus its latency histogram.
type StepExport struct {
	Name   string       `json:"name"`
	Total  int64        `json:"total"`
	Errors int64        `json:"errors"`
	Hist   HistEnvelope `json:"hist"`
}

// GroupExport carries one named group's counters plus its latency histogram.
type GroupExport struct {
	Name   string       `json:"name"`
	Total  int64        `json:"total"`
	Errors int64        `json:"errors"`
	Hist   HistEnvelope `json:"hist"`
}

// HistogramExport is a complete, JSON-serializable snapshot of a Collector's
// state. Workers send this to the coordinator, which merges multiple exports
// into one statistically correct Summary via MergeExports.
type HistogramExport struct {
	Total   int64         `json:"total"`
	Errors  int64         `json:"errors"`
	BytesIn int64         `json:"bytes_in"`
	MinNs   int64         `json:"min_ns"`
	MaxNs   int64         `json:"max_ns"`
	WallNs  int64         `json:"wall_ns"`
	Dropped int64         `json:"dropped"`
	Hist    HistEnvelope  `json:"hist"`
	Steps   []StepExport  `json:"steps,omitempty"`
	Groups  []GroupExport `json:"groups,omitempty"`
	// ErrorBreakdown carries per-cause failure counts so the coordinator can
	// explain failures in plain language across the whole distributed run.
	ErrorBreakdown map[ErrorKind]int64 `json:"error_breakdown,omitempty"`
}

// exportHist converts a live histogram into a serializable envelope.
func exportHist(h *hdrhistogram.Histogram) HistEnvelope {
	s := h.Export()
	return HistEnvelope{
		Lo:     s.LowestTrackableValue,
		Hi:     s.HighestTrackableValue,
		Sig:    s.SignificantFigures,
		Counts: s.Counts,
	}
}

// toHist reconstructs a histogram from an envelope. Returns nil when the
// envelope carries no buckets, so callers can skip merging empty shards.
func (e HistEnvelope) toHist() *hdrhistogram.Histogram {
	if len(e.Counts) == 0 {
		return nil
	}
	return hdrhistogram.Import(&hdrhistogram.Snapshot{
		LowestTrackableValue:  e.Lo,
		HighestTrackableValue: e.Hi,
		SignificantFigures:    e.Sig,
		Counts:                e.Counts,
	})
}

// Export captures the collector's full state as a serializable snapshot.
// Call after Stop() so the histograms are final; safe to call concurrently
// with reads since it takes the read lock.
func (c *Collector) Export() HistogramExport {
	c.mu.RLock()
	defer c.mu.RUnlock()

	exp := HistogramExport{
		Total:   c.sum.Total,
		Errors:  c.sum.Errors,
		BytesIn: c.sum.BytesIn,
		MinNs:   int64(c.sum.MinLatency),
		MaxNs:   int64(c.sum.MaxLatency),
		WallNs:  int64(time.Since(c.startedAt)),
		Dropped: c.dropped.Load(),
		Hist:    exportHist(c.hist),
	}
	if len(c.sum.ErrorBreakdown) > 0 {
		exp.ErrorBreakdown = make(map[ErrorKind]int64, len(c.sum.ErrorBreakdown))
		for k, v := range c.sum.ErrorBreakdown {
			exp.ErrorBreakdown[k] = v
		}
	}
	for _, name := range c.stepOrder {
		s := c.stepSums[name]
		exp.Steps = append(exp.Steps, StepExport{
			Name:   name,
			Total:  s.Total,
			Errors: s.Errors,
			Hist:   exportHist(c.stepHists[name]),
		})
	}
	for _, name := range c.groupOrder {
		s := c.groupSums[name]
		exp.Groups = append(exp.Groups, GroupExport{
			Name:   name,
			Total:  s.Total,
			Errors: s.Errors,
			Hist:   exportHist(c.groupHists[name]),
		})
	}
	return exp
}

// MergeExports combines snapshots from multiple workers into one Summary,
// recomputing percentiles from the merged histograms rather than averaging
// per-worker percentiles. Returns the zero Summary when given no exports.
func MergeExports(exports []HistogramExport) Summary {
	var sum Summary
	if len(exports) == 0 {
		return sum
	}

	merged := hdrhistogram.New(histMinNs, histMaxNs, histSigFigs)

	stepHists := make(map[string]*hdrhistogram.Histogram)
	stepSums := make(map[string]*StepSummary)
	var stepOrder []string

	groupHists := make(map[string]*hdrhistogram.Histogram)
	groupSums := make(map[string]*GroupSummary)
	var groupOrder []string

	for _, e := range exports {
		sum.Total += e.Total
		sum.Errors += e.Errors
		sum.BytesIn += e.BytesIn
		sum.DroppedSamples += e.Dropped

		for k, v := range e.ErrorBreakdown {
			if sum.ErrorBreakdown == nil {
				sum.ErrorBreakdown = make(map[ErrorKind]int64)
			}
			sum.ErrorBreakdown[k] += v
		}

		if e.MinNs > 0 && (sum.MinLatency == 0 || time.Duration(e.MinNs) < sum.MinLatency) {
			sum.MinLatency = time.Duration(e.MinNs)
		}
		if time.Duration(e.MaxNs) > sum.MaxLatency {
			sum.MaxLatency = time.Duration(e.MaxNs)
		}
		// Wall time of a distributed run is the longest single shard, since
		// shards run concurrently rather than sequentially.
		if time.Duration(e.WallNs) > sum.WallTime {
			sum.WallTime = time.Duration(e.WallNs)
		}

		if h := e.Hist.toHist(); h != nil {
			merged.Merge(h)
		}

		for _, st := range e.Steps {
			if stepSums[st.Name] == nil {
				stepSums[st.Name] = &StepSummary{Name: st.Name}
				stepHists[st.Name] = hdrhistogram.New(histMinNs, histMaxNs, histSigFigs)
				stepOrder = append(stepOrder, st.Name)
			}
			stepSums[st.Name].Total += st.Total
			stepSums[st.Name].Errors += st.Errors
			if h := st.Hist.toHist(); h != nil {
				stepHists[st.Name].Merge(h)
			}
		}

		for _, gr := range e.Groups {
			if groupSums[gr.Name] == nil {
				groupSums[gr.Name] = &GroupSummary{Name: gr.Name}
				groupHists[gr.Name] = hdrhistogram.New(histMinNs, histMaxNs, histSigFigs)
				groupOrder = append(groupOrder, gr.Name)
			}
			groupSums[gr.Name].Total += gr.Total
			groupSums[gr.Name].Errors += gr.Errors
			if h := gr.Hist.toHist(); h != nil {
				groupHists[gr.Name].Merge(h)
			}
		}
	}

	if merged.TotalCount() > 0 {
		sum.P50 = nsToD(merged.ValueAtQuantile(50))
		sum.P90 = nsToD(merged.ValueAtQuantile(90))
		sum.P95 = nsToD(merged.ValueAtQuantile(95))
		sum.P99 = nsToD(merged.ValueAtQuantile(99))
	}

	if len(stepOrder) > 0 {
		sum.Steps = make([]StepSummary, 0, len(stepOrder))
		for _, name := range stepOrder {
			ss := stepSums[name]
			if h := stepHists[name]; h.TotalCount() > 0 {
				ss.P50 = nsToD(h.ValueAtQuantile(50))
				ss.P90 = nsToD(h.ValueAtQuantile(90))
				ss.P95 = nsToD(h.ValueAtQuantile(95))
				ss.P99 = nsToD(h.ValueAtQuantile(99))
			}
			sum.Steps = append(sum.Steps, *ss)
		}
	}

	if len(groupOrder) > 0 {
		sum.Groups = make([]GroupSummary, 0, len(groupOrder))
		for _, name := range groupOrder {
			gs := groupSums[name]
			if h := groupHists[name]; h.TotalCount() > 0 {
				gs.P50 = nsToD(h.ValueAtQuantile(50))
				gs.P95 = nsToD(h.ValueAtQuantile(95))
				gs.P99 = nsToD(h.ValueAtQuantile(99))
			}
			sum.Groups = append(sum.Groups, *gs)
		}
	}

	return sum
}
