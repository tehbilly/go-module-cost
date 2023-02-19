package modulecost_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/dustin/go-humanize"
	"github.com/olekukonko/tablewriter"
	modulecost "github.com/tehbilly/go-module-cost"
)

func TestAnalyzer(t *testing.T) {
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"package", "version", "goos", "goarch", "duration", "cost"})

	type testCase struct {
		module  string
		options []modulecost.Option
	}

	testCases := []testCase{
		{options: []modulecost.Option{
			modulecost.WithModule("github.com/fsouza/go-dockerclient"),
			modulecost.WithModule("github.com/docker/docker/client"),
			modulecost.WithGOOS("windows"),
			modulecost.WithGOOS("darwin"),
			modulecost.WithGOOS("linux"),
		}},
	}

	for _, tc := range testCases {
		a, err := modulecost.NewAnalyzer(tc.options...)
		if err != nil {
			t.Error(err)
			t.Fail()
			return
		}

		if r, err := a.Analyze(); err != nil {
			fmt.Printf("Error running a.CostInBytes(): %s\n", err)
			t.Fail()
			return
		} else {
			for _, result := range r {
				tw.Append([]string{
					result.Module,
					result.Version,
					result.GOOS,
					result.GOARCH,
					fmt.Sprint(result.Duration),
					humanize.Bytes(result.Cost),
				})
			}
		}
	}

	tw.Render()
}
