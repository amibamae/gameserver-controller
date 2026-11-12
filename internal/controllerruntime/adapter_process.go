package controllerruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
)

// Out-of-process adapter over jsonrpc-stdio-v1 (`implementation: process`).

// ProcessSettings is the `process:` block. Env is the complete environment for
// the child; nothing is inherited from the controller.
type ProcessSettings struct {
	Path             string   `yaml:"path"`
	Args             []string `yaml:"args"`
	Env              []string `yaml:"env"`
	WorkingDirectory string   `yaml:"working_directory"`
	StartTimeout     string   `yaml:"start_timeout"`
	StopTimeout      string   `yaml:"stop_timeout"`
}

var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func (p *ProcessSettings) Validate(check config.SettingsCheck) {
	check.Require(filepath.IsAbs(p.Path) && !strings.ContainsRune(p.Path, '\x00'), "path must be an absolute executable path")
	check.Require(p.WorkingDirectory == "" || filepath.IsAbs(p.WorkingDirectory), "working_directory must be absolute when set")
	for _, arg := range p.Args {
		check.Require(!strings.ContainsRune(arg, '\x00'), "args must not contain NUL")
	}
	seen := map[string]bool{}
	for _, entry := range p.Env {
		name, _, found := strings.Cut(entry, "=")
		check.Require(found && envNamePattern.MatchString(name), "env entries must be NAME=value with an upper-case name")
		check.Require(!seen[name], "env must not repeat "+name)
		seen[name] = true
	}
	for field, text := range map[string]string{"start_timeout": p.StartTimeout, "stop_timeout": p.StopTimeout} {
		if text == "" {
			continue
		}
		duration, err := time.ParseDuration(text)
		check.Require(err == nil && duration > 0 && duration <= 10*time.Minute, field+" must be greater than zero and no more than 10m")
	}
}

func processConfig(profile config.AdapterProfile, options Options) adapterprocess.Config {
	p, ok := config.SettingsOf[*ProcessSettings](profile)
	if !ok {
		p = &ProcessSettings{}
	}
	start, _ := time.ParseDuration(p.StartTimeout)
	stop, _ := time.ParseDuration(p.StopTimeout)
	return adapterprocess.Config{
		Path: p.Path, Args: p.Args, Env: p.Env, Dir: p.WorkingDirectory,
		ControllerID: "gameserver-controller", ControllerVersion: options.ControllerVersion,
		StartTimeout: start, StopTimeout: stop,
	}
}

func init() {
	registerAdapter(config.Implementation{
		Name:        "process",
		NewSettings: func() config.ImplementationSettings { return &ProcessSettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			path := processConfig(b.profile, b.options).Path
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return errors.New("process.path is not an executable file")
			}
			return nil
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			process, err := adapterprocess.Start(b.ctx, b.schemas, processConfig(b.profile, b.options))
			if err != nil {
				return builtAdapter{}, err
			}
			built := builtAdapter{implementation: process, process: process}
			// A configured executable is not automatically trusted.
			descriptor := process.Descriptor()
			if descriptor.AdapterID != b.profile.ID || string(descriptor.Category) != b.profile.Category {
				return built, fmt.Errorf("process reported identity %q (%s), not the configured one", descriptor.AdapterID, descriptor.Category)
			}
			return built, nil
		},
	})
}
