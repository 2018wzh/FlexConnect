package dist

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type Target interface {
	String() string
	Build(*Build) ([]string, error)
}

type Signer func([]byte) ([]byte, error)

type BuildConfig struct {
	Version            string
	Out                string
	Verbose            bool
	Manifest           string
	WindowsUpgradeCode string
}

type Build struct {
	Repo               string
	Out                string
	Tmp                string
	Version            string
	Verbose            bool
	WindowsUpgradeCode string
	CommonArtifactsFn  func(goos, goarch string) (CommonArtifacts, error)

	mu          sync.Mutex
	binaryCache map[string]string
}

func NewBuild(repo string, cfg BuildConfig) (*Build, error) {
	root, err := findModRoot(repo)
	if err != nil {
		return nil, err
	}
	outDir := cfg.Out
	if outDir == "" {
		outDir = filepath.Join(root, "dist")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(root, outDir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "flexconnect-dist-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	return &Build{
		Repo:               root,
		Out:                outDir,
		Tmp:                tmpDir,
		Version:            cfg.Version,
		Verbose:            cfg.Verbose,
		WindowsUpgradeCode: cfg.WindowsUpgradeCode,
		binaryCache:        map[string]string{},
	}, nil
}

func (b *Build) Close() error {
	return os.RemoveAll(b.Tmp)
}

func (b *Build) Build(targets []Target) ([]string, error) {
	type result struct {
		files []string
		err   error
	}

	results := make([]result, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target Target) {
			defer wg.Done()
			files, err := target.Build(b)
			if err != nil {
				err = fmt.Errorf("%s: %w", target.String(), err)
			}
			results[i] = result{files: files, err: err}
		}(i, target)
	}
	wg.Wait()

	var (
		files []string
		errs  []error
	)
	for _, result := range results {
		files = append(files, result.files...)
		if result.err != nil {
			errs = append(errs, result.err)
		}
	}
	sort.Strings(files)
	return files, errors.Join(errs...)
}

type GoBinaryOptions struct {
	Name    string
	Env     map[string]string
	Ldflags []string
}

func (b *Build) BuildGoBinary(pkg string, opts GoBinaryOptions) (string, error) {
	key := pkg + "|" + opts.Name + "|" + envKey(opts.Env) + "|" + strings.Join(opts.Ldflags, ",")

	b.mu.Lock()
	if cached, ok := b.binaryCache[key]; ok {
		b.mu.Unlock()
		return cached, nil
	}
	b.mu.Unlock()

	outDir, err := os.MkdirTemp(b.Tmp, "gobuild-*")
	if err != nil {
		return "", err
	}
	name := opts.Name
	if name == "" {
		name = filepath.Base(pkg)
	}
	if opts.Env["GOOS"] == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	outPath := filepath.Join(outDir, name)
	args := []string{"build", "-o", outPath}
	if len(opts.Ldflags) > 0 {
		args = append(args, "-ldflags", strings.Join(opts.Ldflags, " "))
	}
	args = append(args, pkg)

	cmd := exec.Command("go", args...)
	cmd.Dir = b.Repo
	cmd.Env = append(os.Environ(), envList(opts.Env)...)
	if b.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return "", err
	}

	b.mu.Lock()
	b.binaryCache[key] = outPath
	b.mu.Unlock()
	return outPath, nil
}

type CommonArtifacts struct {
	FlexConnect  string
	FlexConnectD string
	FlexTray     string
	ServiceFile  string
	LaunchdPlist string
	WintunDLL    string
}

func (b *Build) BuildCommonArtifacts(goos, goarch string) (CommonArtifacts, error) {
	if b.CommonArtifactsFn != nil {
		return b.CommonArtifactsFn(goos, goarch)
	}
	env := map[string]string{
		"GOOS":   goos,
		"GOARCH": goarch,
	}
	flexconnect, err := b.BuildGoBinary("./cmd/flexconnect", GoBinaryOptions{Name: "flexconnect", Env: env})
	if err != nil {
		return CommonArtifacts{}, err
	}
	flexconnectd, err := b.BuildGoBinary("./cmd/flexconnectd", GoBinaryOptions{Name: "flexconnectd", Env: env})
	if err != nil {
		return CommonArtifacts{}, err
	}
	trayOpts := GoBinaryOptions{Name: "flextray", Env: env}
	if goos == "windows" {
		trayOpts.Ldflags = []string{"-H=windowsgui"}
	}
	flextray, err := b.BuildGoBinary("./cmd/flextray", trayOpts)
	if err != nil {
		return CommonArtifacts{}, err
	}
	return CommonArtifacts{
		FlexConnect:  flexconnect,
		FlexConnectD: flexconnectd,
		FlexTray:     flextray,
		ServiceFile:  b.RepoPath("scripts", "systemd", "flexconnectd.service"),
		LaunchdPlist: b.RepoPath("scripts", "launchd", "com.flexconnect.flexconnectd.plist"),
		WintunDLL:    b.RepoPath("assets", "windows", "wintun.dll"),
	}, nil
}

func (b *Build) RepoPath(parts ...string) string {
	all := append([]string{b.Repo}, parts...)
	return filepath.Join(all...)
}

func (b *Build) TmpDir(prefix string) (string, error) {
	return os.MkdirTemp(b.Tmp, prefix)
}

func (b *Build) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (b *Build) WriteManifest(path string, files []string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.Repo, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lines := make([]string, 0, len(files))
	for _, file := range files {
		if !filepath.IsAbs(file) {
			file = filepath.Join(b.Out, file)
		}
		rel, err := filepath.Rel(filepath.Dir(path), file)
		if err != nil {
			return err
		}
		lines = append(lines, rel)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func FilterTargets(targets []Target, filters []string) ([]Target, error) {
	if len(filters) == 0 {
		return append([]Target(nil), targets...), nil
	}
	matchers := make([]*regexp.Regexp, 0, len(filters))
	for _, filter := range filters {
		re, err := globToRegexp(filter)
		if err != nil {
			return nil, err
		}
		matchers = append(matchers, re)
	}
	var out []Target
	for _, target := range targets {
		for _, matcher := range matchers {
			if matcher.MatchString(target.String()) {
				out = append(out, target)
				break
			}
		}
	}
	return out, nil
}

func globToRegexp(filter string) (*regexp.Regexp, error) {
	var pattern strings.Builder
	pattern.WriteString("^")
	for _, ch := range filter {
		switch ch {
		case '*':
			pattern.WriteString(".*")
		case '?':
			pattern.WriteString(".")
		default:
			pattern.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	pattern.WriteString("$")
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, fmt.Errorf("invalid filter %q: %w", filter, err)
	}
	return re, nil
}

func findModRoot(start string) (string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root, nil
		}
		next := filepath.Dir(root)
		if next == root {
			return "", errors.New("could not find go.mod")
		}
		root = next
	}
}

func envKey(env map[string]string) string {
	var pairs []string
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ";")
}

func envList(env map[string]string) []string {
	var pairs []string
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs
}
