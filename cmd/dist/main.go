package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"flexconnect/release/dist"
	"flexconnect/release/dist/darwinpkgs"
	"flexconnect/release/dist/unixpkgs"
	"flexconnect/release/dist/windowspkgs"
)

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}

	switch args[0] {
	case "list":
		return runList(args[1:])
	case "build":
		return runBuild(args[1:])
	case "-h", "--help", "help":
		usage()
		return flag.ErrHelp
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	if err := fs.Parse(args); err != nil {
		return err
	}

	targets := allTargets()
	filtered, err := dist.FilterTargets(targets, fs.Args())
	if err != nil {
		return err
	}
	for _, target := range filtered {
		fmt.Println(target.String())
	}
	return nil
}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	var cfg dist.BuildConfig
	fs.StringVar(&cfg.Version, "version", "", "package version")
	fs.StringVar(&cfg.Out, "out", "", "output directory (defaults to ./dist)")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "enable verbose build logging")
	fs.StringVar(&cfg.Manifest, "manifest", "", "write built artifacts to manifest file")
	fs.StringVar(&cfg.WindowsUpgradeCode, "windows-upgrade-code", "", "override the Windows MSI UpgradeCode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Version == "" {
		return errors.New("build requires --version")
	}

	targets := allTargets()
	filtered, err := dist.FilterTargets(targets, fs.Args())
	if err != nil {
		return err
	}
	if len(filtered) == 0 {
		return errors.New("no targets matched")
	}

	build, err := dist.NewBuild(".", cfg)
	if err != nil {
		return err
	}
	defer build.Close()

	files, err := build.Build(filtered)
	if err != nil {
		return err
	}
	if err := build.WriteManifest(cfg.Manifest, files); err != nil {
		return err
	}
	fmt.Printf("Built %d artifact(s)\n", len(files))
	return nil
}

func allTargets() []dist.Target {
	var targets []dist.Target
	targets = append(targets, unixpkgs.Targets()...)
	targets = append(targets, windowspkgs.Targets()...)
	targets = append(targets, darwinpkgs.Targets()...)
	return targets
}

func usage() {
	fmt.Println("Usage:")
	fmt.Println("  go run ./cmd/dist list [filters]")
	fmt.Println("  go run ./cmd/dist build --version <ver> [--out <dir>] [--verbose] [filters]")
}
