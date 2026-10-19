package agent

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// junitNode is a test suite, or a collection of suites, of a JUnit XML report.
type junitNode struct {
	Suites []junitNode `xml:"testsuite"`
	Cases  []junitCase `xml:"testcase"`
}

// junitCase is one test case; a failure or error child marks it failed.
type junitCase struct {
	Name    string        `xml:"name,attr"`
	Class   string        `xml:"classname,attr"`
	Failure *junitProblem `xml:"failure"`
	Error   *junitProblem `xml:"error"`
}

// junitProblem is the message and text of a failure or error.
type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// ParseJUnit reads JUnit XML into one run per test case; skipped cases pass.
// Text before a report is ignored, several reports one after another are all read, and no test case is an error.
func ParseJUnit(report string) ([]TestRun, error) {
	i := strings.Index(report, "<testsuite")
	if i < 0 {
		return nil, errors.New("no JUnit report in the output")
	}

	var runs []TestRun
	var walk func(n junitNode)
	walk = func(n junitNode) {
		for _, c := range n.Cases {
			r := TestRun{Package: c.Class, Name: c.Name, Passed: true}
			if p := firstProblem(c); p != nil {
				r.Passed = false
				r.Output = CapOutput(strings.TrimSpace(p.Message+"\n"+p.Text), GateOutputCap)
			}
			runs = append(runs, r)
		}
		for _, s := range n.Suites {
			walk(s)
		}
	}

	dec := xml.NewDecoder(strings.NewReader(report[i:]))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode JUnit report: %w", err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok || (start.Name.Local != "testsuite" && start.Name.Local != "testsuites") {
			continue
		}

		var root junitNode
		if err := dec.DecodeElement(&root, &start); err != nil {
			return nil, fmt.Errorf("decode JUnit report: %w", err)
		}
		walk(root)
	}

	if len(runs) == 0 {
		return nil, errors.New("JUnit report has no test case")
	}
	return runs, nil
}

// firstProblem returns the failure or error of a case, or nil when it has neither.
func firstProblem(c junitCase) *junitProblem {
	if c.Failure != nil {
		return c.Failure
	}
	return c.Error
}
