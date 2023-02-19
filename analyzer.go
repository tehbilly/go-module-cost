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

// Result represents the results of analyzing a Module / GOOS / GOARCH combination
type Result struct {
	// Duration records how long it took to perform analysis for this Module / GOOS / GOARCH
	Duration time.Duration

	// Error will be nil unless an error was encountered performing this analysis
	Error error

	// Module is the module path
	Module string

	// Version is the module version as parsed from the generate go.mod file
	Version string

	// GOOS is the GOOS environment variable used for this analysis
	GOOS string

	// GOARCH is the GOARCH environment variable used for this analysis
	GOARCH string

	// Baseline is the size in bytes of the baseline (without any modules added) binary
	Baseline uint64

	// WithMod is the size in bytes of the binary built that includes this module
	WithMod uint64

	// Cost is the result of subtracting Baseline from WithMod
	Cost uint64
}

// Analyzer instances can be used to analyze cost of a matrix of modules under specified GOOS and GOARCH
type Analyzer struct {
	workDir string
	modules []string
	goos    []string
	goarch  []string
}

// Option is used to configure an Analyzer instance
type Option func(a *Analyzer) error

// WithWorkDir will specify a path to use while performing analysis
func WithWorkDir(workDir string) Option {
	return func(a *Analyzer) error {
		if workDir != "" {
			a.workDir = workDir
		}
		return nil
	}
}

// WithModule will add module to list of modules to analyze
func WithModule(module string) Option {
	return func(a *Analyzer) error {
		if module != "" {
			a.modules = append(a.modules, module)
		}
		return nil
	}
}

// WithModules will add modules to list of modules to analyze
func WithModules(modules []string) Option {
	return func(a *Analyzer) error {
		for _, module := range modules {
			if module != "" {
				a.modules = append(a.modules, module)
			}
		}
		return nil
	}
}

// WithModulesFromGoMod will read a list of modules that are required from go.mod at goModPath
func WithModulesFromGoMod(goModPath string) Option {
	return func(a *Analyzer) error {
		goModBytes, err := os.ReadFile(goModPath)
		if err != nil {
			return fmt.Errorf("unable to read go.mod: %w", err)
		}

		parsed, err := modfile.Parse("go.mod", goModBytes, nil)
		if err != nil {
			return fmt.Errorf("unable to parse go.mod: %w", err)
		}

		for _, req := range parsed.Require {
			a.modules = append(a.modules, req.Mod.String())
		}

		return nil
	}
}

// WithGOOS will add the specified GOOS to analysis
func WithGOOS(goos string) Option {
	return func(a *Analyzer) error {
		if goos != "" {
			a.goos = append(a.goos, goos)
		}
		return nil
	}
}

// WithGOOSes will add all specified GOOSes to analysis
func WithGOOSes(gooses []string) Option {
	return func(a *Analyzer) error {
		for _, goos := range gooses {
			if goos != "" {
				a.goos = append(a.goos, goos)
			}
		}
		return nil
	}
}

// WithGOARCH will add the specified GOARCH to analysis
func WithGOARCH(goarch string) Option {
	return func(a *Analyzer) error {
		if goarch != "" {
			a.goarch = append(a.goarch, goarch)
		}
		return nil
	}
}

// WithGOARCHes will add all specified GOARCHes to analysis
func WithGOARCHes(goarches []string) Option {
	return func(a *Analyzer) error {
		for _, goarch := range goarches {
			if goarch != "" {
				a.goarch = append(a.goarch, goarch)
			}
		}
		return nil
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

// NewAnalyzer will create an instance of Analyzer configured using provided options
func NewAnalyzer(options ...Option) (*Analyzer, error) {
	a := &Analyzer{}

	for _, option := range options {
		if err := option(a); err != nil {
			return nil, err
		}
	}

	if err := validate(a); err != nil {
		return nil, err
	}

	return a, nil
}

// Analyze will perform analysis. An error will be returned if a base size is unable to be calculated for a particular
// GOOS and GOARCH pair, otherwise any errors during analysis of a particular module/GOOS/GOARCH will be added to the
// relevant Result
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
