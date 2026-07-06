package reporter

import (
	"encoding/xml"
	"io"

	"github.com/machiko/ramplio/internal/metrics"
)

type junitTestsuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	Testsuites []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Errors   int             `xml:"errors,attr"`
	Time     float64         `xml:"time,attr"`
	Cases    []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
}

// WriteJUnit writes a JUnit XML test report to w.
// scenarioName labels the test case. thresholdMsg is non-empty when a
// threshold was violated and will be recorded as a test failure.
func WriteJUnit(w io.Writer, sum metrics.Summary, scenarioName, thresholdMsg string) error {
	wallSec := sum.WallTime.Seconds()

	tc := junitTestcase{
		Name:      scenarioName,
		Classname: "ramplio",
		Time:      wallSec,
	}
	failures := 0
	if thresholdMsg != "" {
		failures = 1
		tc.Failure = &junitFailure{
			Message: thresholdMsg,
			Type:    "ThresholdViolation",
		}
	}

	root := junitTestsuites{
		Testsuites: []junitTestsuite{{
			Name:     "ramplio",
			Tests:    1,
			Failures: failures,
			Errors:   0,
			Time:     wallSec,
			Cases:    []junitTestcase{tc},
		}},
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	return enc.Flush()
}
