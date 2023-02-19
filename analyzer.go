package modulecost

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dave/jennifer/jen"
	"golang.org/x/mod/modfile"
)

type Result struct {
	// How long it took to perform analysis
	Duration time.Duration

	// If an error was encountered, this will not be nil
	Error error

	// Analysis information
	Module  string
	Version string
	GOOS    string
	GOARCH  string

	// Sizes in bytes
	Baseline uint64
	WithMod  uint64
	Cost     uint64
}

type Analyzer struct {
	workDir string
	modules []string
	goos    []string
	goarch  []string
}

type Option func(a *Analyzer)

func WithWorkDir(workDir string) Option {
	return func(a *Analyzer) {
		if workDir != "" {
			a.workDir = workDir
		}
	}
}

func WithModule(module string) Option {
	return func(a *Analyzer) {
		if module != "" {
			a.modules = append(a.modules, module)
		}
	}
}

func WithModules(modules []string) Option {
	return func(a *Analyzer) {
		for _, module := range modules {
			if module != "" {
				a.modules = append(a.modules, module)
			}
		}
	}
}

func WithGOOS(goos string) Option {
	return func(a *Analyzer) {
		if goos != "" {
			a.goos = append(a.goos, goos)
		}
	}
}

func WithGOOSes(gooses []string) Option {
	return func(a *Analyzer) {
		for _, goos := range gooses {
			if goos != "" {
				a.goos = append(a.goos, goos)
			}
		}
	}
}

func WithGOARCH(goarch string) Option {
	return func(a *Analyzer) {
		if goarch != "" {
			a.goarch = append(a.goarch, goarch)
		}
	}
}

func WithGOARCHes(goarches []string) Option {
	return func(a *Analyzer) {
		for _, goarch := range goarches {
			if goarch != "" {
				a.goarch = append(a.goarch, goarch)
			}
		}
	}
}

func validate(a *Analyzer) error {
	if len(a.modules) == 0 {
		return errors.New("must provide at least one module to analyze")
	}

	if a.workDir == "" {
		a.workDir = filepath.Join(os.TempDir(), "go-module-cost")
	}

	if len(a.goos) == 0 {
		a.goos = append(a.goos, runtime.GOOS)
	}

	if len(a.goarch) == 0 {
		a.goarch = append(a.goarch, runtime.GOARCH)
	}

	return nil
}

func NewAnalyzer(options ...Option) (*Analyzer, error) {
	a := &Analyzer{}

	for _, option := range options {
		option(a)
	}

	if err := validate(a); err != nil {
		return nil, err
	}

	return a, nil
}

func (a *Analyzer) Analyze() ([]*Result, error) {
	baseSizes := map[string]uint64{}
	// Calculate base sizes
	for _, goos := range a.goos {
		for _, goarch := range a.goarch {
			baseSize, err := a.calcBytes(filepath.Join(a.workDir, "base"), "", goos, goarch)
			if err != nil {
				return nil, err
			}
			baseSizes[fmt.Sprintf("%s:%s", goos, goarch)] = baseSize
		}
	}

	var results []*Result

	for _, goos := range a.goos {
		for _, goarch := range a.goarch {
			for _, module := range a.modules {
				result, err := a.analyzeModule(module, goos, goarch)
				if err != nil {
					// TODO: Add logging
					results = append(results, result)
					continue
				}
				baseSize := baseSizes[fmt.Sprintf("%s:%s", goos, goarch)]
				result.Baseline = baseSize
				result.Cost = result.WithMod - baseSize
				results = append(results, result)
			}
		}
	}

	return results, nil
}

func (a *Analyzer) analyzeModule(module string, goos string, goarch string) (*Result, error) {
	start := time.Now()

	workDir := filepath.Join(a.workDir, path.Base(module))
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			fmt.Printf("Unable to remove '%s': %s\n", workDir, err)
		}
	}()

	modSize, err := a.calcBytes(filepath.Join(workDir, "mod"), module, goos, goarch)

	// See if any error occurred calculating bytes
	if err != nil {
		return &Result{
			Duration: time.Since(start),
			Module:   module,
			GOOS:     goos,
			GOARCH:   goarch,
			Error:    err,
		}, err
	}

	modPath, modVersion, err := versionFromModFile(filepath.Join(workDir, "mod", "go.mod"), module)
	if err != nil {
		return &Result{
			Duration: time.Since(start),
			Module:   module,
			GOOS:     goos,
			GOARCH:   goarch,
			Error:    err,
		}, err
	}

	return &Result{
		Duration: time.Since(start),
		Module:   modPath,
		Version:  modVersion,
		GOOS:     goos,
		GOARCH:   goarch,
		WithMod:  modSize,
	}, nil
}

func (a *Analyzer) calcBytes(workDir string, module string, goos string, goarch string) (uint64, error) {
	// Build the directory
	if err := buildModuleDir(workDir, module); err != nil {
		return 0, err
	}

	// Build the binary
	if err := a.buildBin(workDir, goos, goarch); err != nil {
		return 0, err
	}

	// Baseline bytes
	return binBytes(workDir)
}

func (a *Analyzer) buildBin(workDir string, goos string, goarch string) error {
	gg := exec.Command("go", "get", "./...")
	gg.Dir = workDir
	gg.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		fmt.Sprintf("GOOS=%s", goos),
		fmt.Sprintf("GOARCH=%s", goarch),
	)
	if err := gg.Run(); err != nil {
		return err
	}

	bc := exec.Command("go", "build", "-o", fmt.Sprintf("bin%s", os.Getenv("GOEXE")), ".")
	bc.Dir = workDir
	bc.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		fmt.Sprintf("GOOS=%s", goos),
		fmt.Sprintf("GOARCH=%s", goarch),
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
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), mod, 0644); err != nil {
		return err
	}

	// write main.go
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), main, 0644); err != nil {
		return err
	}

	return nil
}

func modFile(module string) ([]byte, error) {
	mf := &modfile.File{}

	moduleName := "github.com/tehbilly/go-module-analyzer"
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
	fb, err := os.ReadFile(modFile)
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
