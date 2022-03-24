package modanalyzer

import (
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/olekukonko/tablewriter"
	"os"
	"sync"
	"testing"
)

func TestNewAnalyzer(t *testing.T) {
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"package", "version", "cost"})

	type testCase struct {
		module  string
		options []Option
	}

	testCases := []testCase{
		{module: "github.com/fsouza/go-dockerclient", options: []Option{}},
		{module: "github.com/docker/docker/client", options: []Option{}},
	}

	var wg sync.WaitGroup
	wg.Add(len(testCases))

	for _, tc := range testCases {
		go func(t *testing.T, tc testCase) {
			defer wg.Done()

			a, err := NewAnalyzer(tc.module, tc.options...)
			if err != nil {
				t.Error(err)
				t.Fail()
				return
			}

			if r, err := a.CostInBytes(); err != nil {
				fmt.Printf("Error running a.CostInBytes(): %s\n", err)
				t.Fail()
				return
			} else {
				//fmt.Printf("Cost in bytes for %s@%s: %s (took: %s)\n", r.Module, r.Version, humanize.Bytes(r.Cost), r.Duration)
				tw.Append([]string{r.Module, r.Version, humanize.Bytes(r.Cost)})
			}
		}(t, tc)
	}

	wg.Wait()

	tw.Render()
}
