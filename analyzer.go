package modanalyzer

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/dave/jennifer/jen"
	"golang.org/x/mod/modfile"
)

// TODO: Allow for multiple modules instead of just one. We could base it on GOOS/GOARCH, establish a baseline once,
//       then calculate the cost for each module or specific modules on-demand.

type Result struct {
	// How long it took to perform analysis
	Duration time.Duration

	// Analysis information
	Module  string
	Version string

	// Sizes in bytes
	Baseline uint64
	WithMod  uint64
	Cost     uint64
}

type Analyzer struct {
	goos   string
	goarch string
	module string
}

type Option func(a *Analyzer)

func WithGOOS(goos string) Option {
	return func(a *Analyzer) {
		a.goos = goos
	}
}

func WithGOARCH(goarch string) Option {
	return func(a *Analyzer) {
		a.goarch = goarch
	}
}

func NewAnalyzer(module string, options ...Option) (*Analyzer, error) {
	if module == "" {
		return nil, errors.New("must specify module name")
	}

	a := &Analyzer{
		module: module,
	}

	for _, option := range options {
		option(a)
	}

	// TODO: Make these work
	if a.goos == "" {
		a.goos = runtime.GOOS
	}

	// TODO: Make these work
	if a.goarch == "" {
		a.goarch = runtime.GOARCH
	}

	return a, nil
}

func (a *Analyzer) CostInBytes() (*Result, error) {
	start := time.Now()

	workDir := filepath.Join(os.TempDir(), "go-module-Analyzer", path.Base(a.module))
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			fmt.Printf("Unable to remove '%s': %s\n", workDir, err)
		}
	}()

	var wg sync.WaitGroup

	var err error
	var baseSize uint64
	var modSize uint64

	// Baseline binary size
	wg.Add(1)
	go func() {
		defer wg.Done()
		baseSize, err = a.calcBytes(filepath.Join(workDir, "base"), "")
	}()

	// Size of binary with module
	wg.Add(1)
	go func() {
		defer wg.Done()
		modSize, err = a.calcBytes(filepath.Join(workDir, "mod"), a.module)
	}()

	wg.Wait()

	// See if any error occurred calculating bytes
	if err != nil {
		return nil, err
	}

	modPath, modVersion, err := versionFromModFile(filepath.Join(workDir, "mod", "go.mod"), a.module)
	if err != nil {
		return nil, err
	}

	return &Result{
		Duration: time.Since(start),
		Module:   modPath,
		Version:  modVersion,
		Baseline: baseSize,
		WithMod:  modSize,
		Cost:     modSize - baseSize,
	}, nil
}

func (a *Analyzer) calcBytes(workDir string, module string) (uint64, error) {
	// Build the directory
	if err := buildModuleDir(workDir, module); err != nil {
		return 0, err
	}

	// Build the binary
	if err := a.buildBin(workDir); err != nil {
		return 0, err
	}

	// Baseline bytes
	return binBytes(workDir)
}

func (a *Analyzer) buildBin(workDir string) error {
	gg := exec.Command("go", "get", "./...")
	gg.Dir = workDir
	gg.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		fmt.Sprintf("GOOS=%s", a.goos),
		fmt.Sprintf("GOARCH=%s", a.goarch),
	)
	if err := gg.Run(); err != nil {
		return err
	}

	bc := exec.Command("go", "build", "-o", fmt.Sprintf("bin%s", os.Getenv("GOEXE")), ".")
	bc.Dir = workDir
	bc.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		fmt.Sprintf("GOOS=%s", a.goos),
		fmt.Sprintf("GOARCH=%s", a.goarch),
	)
	if err := bc.Run(); err != nil {
		return err
	}

	return nil
}

func buildModuleDir(workDir string, module string) error {
	mod, err := modFile(module)
	if err != nil {
		return err
	}

	main, err := mainFile(module)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(workDir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}

	// write go.mod
	if err := ioutil.WriteFile(filepath.Join(workDir, "go.mod"), mod, 0644); err != nil {
		return err
	}

	// write main.go
	if err := ioutil.WriteFile(filepath.Join(workDir, "main.go"), main, 0644); err != nil {
		return err
	}

	return nil
}

func modFile(module string) ([]byte, error) {
	mf := &modfile.File{}

	moduleName := "github.com/tehbilly/go-module-Analyzer"
	if module != "" {
		moduleName = moduleName + "/" + path.Base(module)
	}

	if err := mf.AddModuleStmt(moduleName); err != nil {
		return nil, err
	}

	return mf.Format()
}

func mainFile(module string) ([]byte, error) {
	f := jen.NewFilePath("output/main")

	if module != "" {
		f.Anon(module)
	}

	// Add main
	f.Func().Id("main").Params().Block(
		jen.Qual("fmt", "Println").Call(jen.Lit("Hello!")),
	)

	var bb bytes.Buffer
	if err := f.Render(&bb); err != nil {
		return nil, err
	}

	return bb.Bytes(), nil
}

func versionFromModFile(modFile string, module string) (string, string, error) {
	fb, err := ioutil.ReadFile(modFile)
	if err != nil {
		return module, "", err
	}

	mf, err := modfile.Parse(fmt.Sprintf("%s/%s", filepath.Dir(modFile), filepath.Base(modFile)), fb, nil)
	if err != nil {
		return module, "", err
	}

	for _, r := range mf.Require {
		if r.Mod.Path == module || !r.Indirect {
			return r.Mod.Path, r.Mod.Version, nil
		}
	}

	return module, "<unknown>", nil
}

func binBytes(workDir string) (uint64, error) {
	binFile := filepath.Join(workDir, fmt.Sprintf("bin%s", os.Getenv("GOEXE")))

	fi, err := os.Stat(binFile)
	if err != nil {
		return 0, err
	}

	if fi.IsDir() {
		return 0, errors.New("binBytes called on a directory, not a file: " + binFile)
	}

	return uint64(fi.Size()), nil
}
