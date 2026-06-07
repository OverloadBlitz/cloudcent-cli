package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OverloadBlitz/cloudcent-cli/internal/api"
	"github.com/OverloadBlitz/cloudcent-cli/internal/estimate"
	internalpulumi "github.com/OverloadBlitz/cloudcent-cli/internal/pulumi"
	pulumidiag "github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"
	pulumicfg "github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	pulumiworkspace "github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var pulumiCmd = &cobra.Command{
	Use:   "pulumi",
	Short: "Pulumi cost estimation (no credentials needed)",
}

var pulumiEstimatePriceFilter string
var pulumiEstimateStack string
var pulumiEstimateConfigFlags []string
var pulumiEstimateUsageFlags []string
var pulumiEstimateModelFlags []string
var pulumiEstimateOutput string

var pulumiEstimateCmd = &cobra.Command{
	Use:   "estimate [path]",
	Short: "Estimate costs for a Pulumi project using mock interception",
	Long: `Compiles and runs the Pulumi program in the target directory (defaults to current
directory) against a local mock gRPC server. No AWS credentials or Pulumi
account required — resources are intercepted before any cloud API is called.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPulumiEstimate,
}

func init() {
	pulumiCmd.AddCommand(pulumiEstimateCmd)
	pulumiEstimateCmd.Flags().StringVar(&pulumiEstimatePriceFilter, "price", "", "Batch price filter for Pulumi estimate requests, e.g. \">=0.2\"")
	pulumiEstimateCmd.Flags().StringVar(&pulumiEstimateStack, "stack", "", "Pulumi stack name to load config from, e.g. \"dev\"")
	pulumiEstimateCmd.Flags().StringArrayVar(&pulumiEstimateConfigFlags, "config", nil, "Set a config value, e.g. --config cfg:autoscalingGroupSize=1 (can be repeated)")
	pulumiEstimateCmd.Flags().StringArrayVar(&pulumiEstimateUsageFlags, "usage", nil, "Monthly usage for a usage-based resource, e.g. --usage my-api=5000000 (can be repeated)")
	pulumiEstimateCmd.Flags().StringArrayVar(&pulumiEstimateModelFlags, "model", nil, "Pricing model override, e.g. --model \"Reserved:standard:1yr\" or --model \"my-ec2=spot\" (can be repeated)")
	pulumiEstimateCmd.Flags().StringVarP(&pulumiEstimateOutput, "output", "o", "table", "Output format: table or json")
}

// pulumiYAML is the minimal subset of Pulumi.yaml we need.
type pulumiYAML struct {
	Runtime  pulumiRuntime       `yaml:"runtime"`
	Template *pulumiYAMLTemplate `yaml:"template"`
}

type pulumiYAMLTemplate struct {
	Config map[string]pulumiYAMLTemplateConfig `yaml:"config"`
}

type pulumiYAMLTemplateConfig struct {
	Default string `yaml:"default"`
}

type pulumiRuntime struct {
	name    string
	Options struct {
		Virtualenv string `yaml:"virtualenv"`
	}
}

func (r *pulumiRuntime) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try plain string first: runtime: go
	var s string
	if err := unmarshal(&s); err == nil {
		r.name = s
		return nil
	}
	// Try object: runtime: {name: python, options: {virtualenv: venv}}
	var obj struct {
		Name    string `yaml:"name"`
		Options struct {
			Virtualenv string `yaml:"virtualenv"`
		} `yaml:"options"`
	}
	if err := unmarshal(&obj); err != nil {
		return err
	}
	r.name = obj.Name
	r.Options.Virtualenv = obj.Options.Virtualenv
	return nil
}

type pulumiProjectInfo struct {
	Name       string
	Runtime    string
	Virtualenv string
	Stack      string
	Config     map[string]string
}

func runPulumiEstimate(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	jsonMode := pulumiEstimateOutput == "json"
	logf := func(format string, a ...any) {
		if jsonMode {
			fmt.Fprintf(os.Stderr, format, a...)
		} else {
			fmt.Printf(format, a...)
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	//Detect Pulumi project
	projectInfo, err := detectPulumiProject(absDir)
	if err != nil {
		return err
	}

	// Apply --config flags on top of whatever was loaded from stack files.
	if len(pulumiEstimateConfigFlags) > 0 {
		if projectInfo.Config == nil {
			projectInfo.Config = make(map[string]string)
		}
		for _, kv := range pulumiEstimateConfigFlags {
			idx := strings.IndexByte(kv, '=')
			if idx < 0 {
				return fmt.Errorf("invalid --config value %q: expected key=value", kv)
			}
			projectInfo.Config[kv[:idx]] = kv[idx+1:]
		}
	}

	logf("Detected Pulumi project %q (runtime: %s, stack: %s) in %s\n", projectInfo.Name, projectInfo.Runtime, projectInfo.Stack, absDir)

	// Start mock gRPC servers
	srv, err := internalpulumi.StartMockGRPCServer()
	if err != nil {
		return fmt.Errorf("starting mock server: %w", err)
	}
	defer srv.Stop()
	// Pass stack config
	if len(projectInfo.Config) > 0 {
		srv.Collector.SetStackConfig(projectInfo.Config)
	}
	logf("Mock monitor listening on %s\n", srv.MonitorAddr)

	// logWriter is the destination for subprocess stdout/stderr output.
	logWriter := io.Writer(os.Stdout)
	if jsonMode {
		logWriter = os.Stderr
	}

	// Ensure dependencies are installed.
	venvCreatedByUs := false
	if projectInfo.Runtime == "python" {
		if jsonMode {
			// Use a unique temp venv name to avoid colliding with any existing venv.
			projectInfo.Virtualenv = fmt.Sprintf("venv-cloudcent-%d", time.Now().UnixNano())
			venvCreatedByUs = true
		} else {
			venvPath := filepath.Join(absDir, pythonVenvDir(projectInfo.Virtualenv))
			if _, err := os.Stat(venvPath); os.IsNotExist(err) {
				// Venv doesn't exist yet — if we create it, we own it.
				venvCreatedByUs = true
			}
		}
	}

	// Register cleanup before attempting installation so the venv is removed
	if venvCreatedByUs {
		venvDir := pythonVenvDir(projectInfo.Virtualenv)
		defer func() {
			venvPath := filepath.Join(absDir, venvDir)
			logf("Cleaning up venv at %s\n", venvPath)
			os.RemoveAll(venvPath)
		}()
	}

	needsInstall, installDesc := checkDependencies(projectInfo.Runtime, absDir, projectInfo.Virtualenv)
	if needsInstall {
		if jsonMode {
			logf("\nAuto-installing dependencies: %s\n", installDesc)
			if err := ensureDependenciesTo(projectInfo.Runtime, absDir, projectInfo.Virtualenv, logWriter); err != nil {
				return fmt.Errorf("installing dependencies: %w", err)
			}
		} else {
			logf("\nDependencies needed: %s\n", installDesc)
			logf("Install now? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				return fmt.Errorf("aborted — install dependencies manually and retry")
			}
			if err := ensureDependenciesTo(projectInfo.Runtime, absDir, projectInfo.Virtualenv, logWriter); err != nil {
				return fmt.Errorf("installing dependencies: %w", err)
			}
		}
	}
	//Pre-scan source files for required config keys and auto-fill non-pricing
	scannedKeys := scanRequiredConfigKeys(absDir, projectInfo.Name, projectInfo.Runtime)
	if len(scannedKeys) > 0 {
		if projectInfo.Config == nil {
			projectInfo.Config = make(map[string]string)
		}
		var filledKeys []string
		var pricingKeys []string
		for _, k := range scannedKeys {
			if projectInfo.Config[k] != "" {
				continue
			}
			if isPricingRelevantConfigKey(k) {
				pricingKeys = append(pricingKeys, k)
				continue
			}
			dummy := dummyValueForConfigKey(k)
			projectInfo.Config[k] = dummy
			filledKeys = append(filledKeys, k)
		}
		if len(filledKeys) > 0 {
			logf("\nAuto-filled %d non-pricing config key(s) with dummy values:\n", len(filledKeys))
			for _, k := range filledKeys {
				logf("  %s = %s\n", k, projectInfo.Config[k])
			}
			logf("\n")
		}
		if len(pricingKeys) > 0 {
			logf("Note: %d pricing-relevant config key(s) still need manual values:\n", len(pricingKeys))
			for _, k := range pricingKeys {
				logf("  • %s\n", k)
			}
			logf("\nRe-run with --config for each key, e.g.:\n  %s pulumi estimate", os.Args[0])
			for _, k := range pricingKeys {
				logf(" --config %s=<value>", k)
			}
			logf("\n")
			return fmt.Errorf("missing pricing-relevant config — see above")
		}
	}

	// Update stack config on the collector after auto-fill.
	if len(projectInfo.Config) > 0 {
		srv.Collector.SetStackConfig(projectInfo.Config)
	}

	// Run the user program.
	if err := runUserProgramTo(projectInfo, absDir, srv.MonitorAddr, srv.EngineAddr, logWriter); err != nil {
		return fmt.Errorf("running user program: %w", err)
	}

	// Wait for the program to finish registering all resources
	srv.Collector.Wait()

	// Propagate TaskDefinition attributes (cpu, memory, runtimePlatform) into
	// the MockedProperties of any ECS Service that references them, so the
	// decoder can read them without re-traversing the resource graph.
	srv.Collector.InjectECSCrossResourceAttrs()

	// 7. Print collected resources
	resources := srv.Collector.Resources
	if len(resources) == 0 {
		logf("No resources detected.\n")
		return nil
	}

	// Load metadata for data-driven resource decoding.
	meta, err := api.LoadMetadataFromFile()
	if err != nil {
		return fmt.Errorf("failed to load metadata (run 'cloudcent metadata refresh' first): %w", err)
	}

	decodedResources := internalpulumi.DecodeAllResources(resources, meta)
	for i := range decodedResources {
		if strings.TrimSpace(pulumiEstimatePriceFilter) != "" {
			decodedResources[i].PriceFilter = strings.TrimSpace(pulumiEstimatePriceFilter)
		}
		// Apply region fallback: if no region was detected, use a provider-appropriate
		// default and flag it.
		// Skip free/no-pricing resources — they don't need region for pricing.
		if !decodedResources[i].NoPricing && strings.TrimSpace(decodedResources[i].Region) == "" {
			fallbackRegion := "us-east-1"
			if decodedResources[i].Provider == "azure" {
				fallbackRegion = "us-east"
			}
			decodedResources[i].Region = fallbackRegion
			decodedResources[i].RegionFallback = true
		}
	}

	if len(decodedResources) == 0 {
		logf("No resources detected.\n")
		return nil
	}

	// Parse --usage flags into a map: resource-name → monthly quantity.
	usageMap := parseUsageFlags(pulumiEstimateUsageFlags)
	// Parse --model flags into a map: resource-name → ModelSelector.
	modelMap := parseModelFlags(pulumiEstimateModelFlags)

	logf("\n=== Resources to be created: %d ===\n", len(decodedResources))
	client, apiError := api.New()
	if apiError != nil {
		return fmt.Errorf("pricing api error: %w", err)
	}
	estimatedResources, estimateError := estimate.EstimateAllResources(client, decodedResources, usageMap, modelMap)
	if estimateError != nil {
		return fmt.Errorf("estimating resources: %w", estimateError)
	}

	if jsonMode {
		estimate.PrintResultsJSON(estimatedResources)
	} else {
		estimate.PrintResults(estimatedResources)
	}

	return nil
}

func detectRuntime(dir string) (string, string, map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
	if err != nil {
		return "", "", nil, fmt.Errorf("no Pulumi.yaml found in %s — is this a Pulumi project?", dir)
	}
	var p pulumiYAML
	if err := yaml.Unmarshal(data, &p); err != nil {
		return "", "", nil, fmt.Errorf("parsing Pulumi.yaml: %w", err)
	}
	if p.Runtime.name == "" {
		return "", "", nil, fmt.Errorf("Pulumi.yaml has no runtime field")
	}

	// Collect template config defaults (e.g. aws:region default: us-west-2).
	var templateDefaults map[string]string
	if p.Template != nil && len(p.Template.Config) > 0 {
		templateDefaults = make(map[string]string, len(p.Template.Config))
		for key, cfg := range p.Template.Config {
			if cfg.Default != "" {
				templateDefaults[key] = cfg.Default
			}
		}
	}

	return p.Runtime.name, p.Runtime.Options.Virtualenv, templateDefaults, nil
}

func detectPulumiProject(dir string) (pulumiProjectInfo, error) {
	runtime, venv, templateDefaults, err := detectRuntime(dir)
	if err != nil {
		return pulumiProjectInfo{}, err
	}

	project, err := pulumiworkspace.LoadProject(filepath.Join(dir, "Pulumi.yaml"))
	if err != nil {
		return pulumiProjectInfo{}, fmt.Errorf("loading Pulumi project: %w", err)
	}

	stack := resolvePulumiStackName(dir, project.StackConfigDir)
	configValues, err := loadPulumiStackConfig(dir, project, stack)
	if err != nil {
		return pulumiProjectInfo{}, err
	}

	// Merge template defaults as a fallback: stack config wins, template defaults fill gaps.
	if len(templateDefaults) > 0 {
		if configValues == nil {
			configValues = make(map[string]string)
		}
		for key, val := range templateDefaults {
			if _, exists := configValues[key]; !exists {
				configValues[key] = val
			}
		}
	}

	if len(configValues) > 0 {
		fmt.Fprintf(os.Stderr, "Resolved config: %v\n", configValues)
	}

	return pulumiProjectInfo{
		Name:       string(project.Name),
		Runtime:    runtime,
		Virtualenv: venv,
		Stack:      stack,
		Config:     configValues,
	}, nil
}

func resolvePulumiStackName(dir, stackConfigDir string) string {
	if stack := strings.TrimSpace(pulumiEstimateStack); stack != "" {
		return stack
	}
	if stack := strings.TrimSpace(os.Getenv("PULUMI_STACK")); stack != "" {
		return stack
	}

	stackNames, err := listPulumiStackNames(dir, stackConfigDir)
	if err != nil || len(stackNames) == 0 {
		return "estimate"
	}
	if len(stackNames) == 1 {
		return stackNames[0]
	}

	for _, preferred := range []string{"dev", "development", "stage", "staging", "prod", "production"} {
		for _, stackName := range stackNames {
			if stackName == preferred {
				return stackName
			}
		}
	}

	return stackNames[0]
}

func listPulumiStackNames(dir, stackConfigDir string) ([]string, error) {
	stackDir := dir
	if stackConfigDir != "" {
		stackDir = filepath.Join(dir, stackConfigDir)
	}

	entries, err := os.ReadDir(stackDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var stackNames []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == "Pulumi.yaml" {
			continue
		}
		if !strings.HasPrefix(name, "Pulumi.") || !strings.HasSuffix(name, ".yaml") {
			continue
		}

		stackName := strings.TrimSuffix(strings.TrimPrefix(name, "Pulumi."), ".yaml")
		if stackName == "" {
			continue
		}
		stackNames = append(stackNames, stackName)
	}

	sort.Strings(stackNames)
	return stackNames, nil
}

func loadPulumiStackConfig(dir string, project *pulumiworkspace.Project, stack string) (map[string]string, error) {
	if strings.TrimSpace(stack) == "" {
		return nil, nil
	}

	sink := pulumidiag.DefaultSink(io.Discard, io.Discard, pulumidiag.FormatOptions{Color: colors.Never})
	stackPath := pulumiStackPath(dir, project.StackConfigDir, stack)
	projectStack, err := pulumiworkspace.LoadProjectStack(sink, project, stackPath)
	if err != nil {
		return nil, fmt.Errorf("loading Pulumi stack config %q: %w", stack, err)
	}
	if len(projectStack.Config) == 0 {
		return nil, nil
	}

	configValues := make(map[string]string)
	for key, value := range projectStack.Config {
		if value.Secure() {
			continue
		}
		raw, err := value.Value(pulumicfg.NopDecrypter)
		if err != nil {
			return nil, fmt.Errorf("reading Pulumi config %q: %w", key.String(), err)
		}
		configValues[key.String()] = raw
	}

	if len(configValues) == 0 {
		return nil, nil
	}

	return configValues, nil
}

func pulumiStackPath(dir, stackConfigDir, stack string) string {
	stackDir := dir
	if stackConfigDir != "" {
		stackDir = filepath.Join(dir, stackConfigDir)
	}

	fileName := fmt.Sprintf("Pulumi.%s.yaml", strings.ReplaceAll(stack, "/", "-"))
	return filepath.Join(stackDir, fileName)
}

func runUserProgram(projectInfo pulumiProjectInfo, dir, monitorAddr, engineAddr string) error {
	return runUserProgramTo(projectInfo, dir, monitorAddr, engineAddr, os.Stdout)
}

func runUserProgramTo(projectInfo pulumiProjectInfo, dir, monitorAddr, engineAddr string, logWriter io.Writer) error {
	env := append(os.Environ(),
		"PULUMI_MONITOR="+monitorAddr,
		"PULUMI_ENGINE="+engineAddr,
		"PULUMI_PROJECT="+projectInfo.Name,
		"PULUMI_STACK="+projectInfo.Stack,
		"PULUMI_ROOT_DIRECTORY="+dir,
		"PULUMI_DRY_RUN=true",
		"PULUMI_PARALLEL=1",
		"PULUMI_ORGANIZATION=cloudcent",
		// Prevent Docker SDK from connecting to a real daemon — point it at
		// a non-existent socket so docker.Image fails fast instead of hanging.
		"DOCKER_HOST=tcp://127.0.0.1:0",
	)
	if len(projectInfo.Config) > 0 {
		configJSON, err := json.Marshal(projectInfo.Config)
		if err != nil {
			return fmt.Errorf("marshaling Pulumi config: %w", err)
		}
		env = append(env, "PULUMI_CONFIG="+string(configJSON))
	}

	var c *exec.Cmd
	switch projectInfo.Runtime {
	case "go":
		c = exec.Command("go", "run", ".")
	case "nodejs", "node":
		// Use the Pulumi Node.js runner (node_modules/@pulumi/pulumi/cmd/run/index.js)
		// instead of ts-node directly. The runner calls settings.configure() with the
		// monitor/engine addresses before executing user code — running ts-node directly
		// leaves the monitor unset, causing resource/invoke calls to fail.
		nodeRunner := filepath.Join(dir, "node_modules", "@pulumi", "pulumi", "cmd", "run", "index.js")
		// Resolve the TypeScript entry point. The runner needs an explicit .ts file
		// (not ".") so it can register ts-node before attempting to load the module.
		// Without this, it tries require(".") → no index.js → fails before ts-node runs.
		tsEntry, err := resolveNodeEntry(dir)
		if err != nil {
			return err
		}
		c = exec.Command("node", nodeRunner,
			"--monitor", monitorAddr,
			"--engine", engineAddr,
			"--project", projectInfo.Name,
			"--stack", projectInfo.Stack,
			"--parallel", "1",
			"--dry-run", "true",
			"--organization", "cloudcent",
			"--pwd", dir,
			tsEntry,
		)
		// Tell the runner this is a TypeScript project so it registers ts-node.
		env = append(env, "PULUMI_NODEJS_TYPESCRIPT=true")
	case "python":
		venvDir := pythonVenvDir(projectInfo.Virtualenv)
		// pulumi-language-python-exec is a system binary (installed with the Pulumi CLI,
		// not the Python SDK). It calls pulumi.runtime.configure(settings) before running
		// user code, which wires up the monitor/engine gRPC stubs. Running __main__.py
		// directly skips this and leaves monitor=None, causing invoke calls to crash.
		langExec, err := exec.LookPath("pulumi-language-python-exec")
		if err != nil {
			return fmt.Errorf("pulumi-language-python-exec not found on PATH — is the Pulumi CLI installed?")
		}
		c = exec.Command(langExec,
			"--monitor", monitorAddr,
			"--engine", engineAddr,
			"--project", projectInfo.Name,
			"--stack", projectInfo.Stack,
			"--parallel", "1",
			"--dry_run", "true",
			"--organization", "cloudcent",
			"--pwd", dir,
			"__main__.py",
		)
		// Prepend the venv's bin dir to PATH so pulumi-language-python-exec picks up
		// the venv's python interpreter and installed packages.
		venvBin := filepath.Join(dir, venvDir, "bin")
		env = prependPath(env, venvBin)
		// Ensure the project directory is on PYTHONPATH so local modules (e.g.
		// `import iam` from a sibling iam.py) can be resolved.
		env = prependPythonPath(env, dir)
	case "dotnet":
		// pulumi-language-dotnet is the official .NET language host. It calls
		// pulumi.runtime.configure() before running user code, wiring up the
		// monitor/engine gRPC stubs. Running `dotnet run` directly skips this
		// and leaves the monitor unset, causing resource registration to fail.
		dotnetLangHost, err := exec.LookPath("pulumi-language-dotnet")
		if err != nil {
			return fmt.Errorf("pulumi-language-dotnet not found on PATH — is the Pulumi CLI installed?")
		}
		c = exec.Command(dotnetLangHost,
			"--monitor", monitorAddr,
			"--engine", engineAddr,
			"--project", projectInfo.Name,
			"--stack", projectInfo.Stack,
			"--parallel", "1",
			"--dry-run", "true",
			"--organization", "cloudcent",
			"--pwd", dir,
		)
	case "java":
		// The Java SDK reads monitor/engine addresses from environment variables
		// (PULUMI_MONITOR, PULUMI_ENGINE, etc.) — the same pattern as Go.
		// We detect whether the project uses Maven (pom.xml) or Gradle (build.gradle).
		if fileExists(filepath.Join(dir, "pom.xml")) {
			c = exec.Command("mvn", "-q", "compile", "exec:java")
		} else if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
			c = exec.Command("gradle", "-q", "run")
		} else {
			return fmt.Errorf("java runtime: no pom.xml or build.gradle found in %s", dir)
		}
	case "yaml":
		// pulumi-language-yaml is the YAML language host. Unlike other runtimes,
		// it is a gRPC server that evaluates the YAML template itself and registers
		// resources directly — there is no user subprocess to launch.
		// We invoke it in "run" mode by passing the engine address as a positional arg.
		yamlLangHost, err := exec.LookPath("pulumi-language-yaml")
		if err != nil {
			return fmt.Errorf("pulumi-language-yaml not found on PATH — is the Pulumi CLI installed?")
		}
		c = exec.Command(yamlLangHost, engineAddr)
	default:
		return fmt.Errorf("unsupported runtime %q", projectInfo.Runtime)
	}

	c.Dir = dir
	c.Env = env
	c.Stdout = logWriter

	// Capture stderr while still printing it, so callers can inspect error
	// messages (e.g. missing required configuration variables).
	var stderrBuf bytes.Buffer
	c.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	// Run with a timeout. Some projects use resources (e.g. docker.Image,
	// mysql.Provider) that attempt real side effects which hang in a mock
	// environment. We give the program a generous timeout and treat a
	// timeout as a partial success — resources registered before the hang
	// are still usable for cost estimation.
	const programTimeout = 60 * time.Second
	done := make(chan error, 1)
	if err := c.Start(); err != nil {
		return &programError{cause: err, stderr: ""}
	}
	go func() {
		done <- c.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			return &programError{cause: err, stderr: stderrBuf.String()}
		}
		return nil
	case <-time.After(programTimeout):
		// Kill the hung process.
		c.Process.Kill()
		<-done // reap
		fmt.Fprintf(logWriter, "\n⚠ Program timed out after %s (likely due to docker.Image, mysql.Provider, or similar side-effect resources).\n", programTimeout)
		fmt.Fprintln(logWriter, "  Resources registered before the timeout will still be used for estimation.")
		return nil // treat as partial success
	}
}

// programError wraps a user-program execution failure and carries the captured stderr.
type programError struct {
	cause  error
	stderr string
}

func (e *programError) Error() string { return e.cause.Error() }
func (e *programError) Unwrap() error { return e.cause }

// hasUV reports whether the `uv` package manager is available on PATH.
func hasUV() bool {
	_, err := exec.LookPath("uv")
	return err == nil
}

// pythonVenvDir returns the effective venv directory name for a Python project.
func pythonVenvDir(venv string) string {
	if venv != "" {
		return venv
	}
	return "venv"
}

// checkDependencies returns whether installation is needed and a human-readable description.
func checkDependencies(runtime, dir, venv string) (needed bool, desc string) {
	switch runtime {
	case "go":
		if _, err := os.Stat(filepath.Join(dir, "go.sum")); os.IsNotExist(err) {
			return false, ""
		}
		c := exec.Command("go", "mod", "verify")
		c.Dir = dir
		if err := c.Run(); err != nil {
			return true, "Go modules (go mod download)"
		}
		return false, ""

	case "nodejs", "node":
		if _, err := os.Stat(filepath.Join(dir, "node_modules", "@pulumi", "pulumi", "cmd", "run", "index.js")); os.IsNotExist(err) {
			return true, "Node.js packages (npm install)"
		}
		return false, ""

	case "python":
		venvPath := filepath.Join(dir, pythonVenvDir(venv))
		venvPython := filepath.Join(venvPath, "bin", "python3")
		if _, err := os.Stat(venvPython); os.IsNotExist(err) {
			tool := "pip"
			if hasUV() {
				tool = "uv"
			}
			src := "requirements.txt"
			if fileExists(filepath.Join(dir, "pyproject.toml")) {
				src = "pyproject.toml"
			}
			extra := ""
			if venv == "" {
				extra = " — a 'venv' folder will be created and removed after"
			}
			return true, fmt.Sprintf("Python packages from %s (%s)%s", src, tool, extra)
		}
		return false, ""

	case "dotnet":
		if _, err := os.Stat(filepath.Join(dir, "obj")); os.IsNotExist(err) {
			return true, ".NET packages (dotnet restore)"
		}
		return false, ""

	case "java":
		if fileExists(filepath.Join(dir, "pom.xml")) {
			// Check if Maven has already downloaded dependencies (local .m2 or target/classes).
			if _, err := os.Stat(filepath.Join(dir, "target", "classes")); os.IsNotExist(err) {
				return true, "Java dependencies (mvn compile)"
			}
		} else if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
			if _, err := os.Stat(filepath.Join(dir, "build", "classes")); os.IsNotExist(err) {
				return true, "Java dependencies (gradle classes)"
			}
		}
		return false, ""

	case "yaml":
		// YAML projects have no dependencies to install.
		return false, ""
	}
	return false, ""
}

// ensureDependencies installs missing dependencies for the given runtime.
func ensureDependencies(runtime, dir, venv string) error {
	return ensureDependenciesTo(runtime, dir, venv, os.Stdout)
}

// ensureDependenciesTo installs missing dependencies, writing progress to w.
func ensureDependenciesTo(runtime, dir, venv string, w io.Writer) error {
	switch runtime {
	case "go":
		fmt.Fprintln(w, "Downloading Go modules…")
		c := exec.Command("go", "mod", "download")
		c.Dir = dir
		c.Stdout = w
		c.Stderr = os.Stderr
		return c.Run()

	case "nodejs", "node":
		fmt.Fprintln(w, "Running npm install…")
		c := exec.Command("npm", "install")
		c.Dir = dir
		c.Stdout = w
		c.Stderr = os.Stderr
		return c.Run()

	case "python":
		return ensurePythonDependenciesTo(dir, venv, w)

	case "dotnet":
		fmt.Fprintln(w, "Restoring .NET packages…")
		c := exec.Command("dotnet", "restore")
		c.Dir = dir
		c.Stdout = w
		c.Stderr = os.Stderr
		return c.Run()

	case "java":
		if fileExists(filepath.Join(dir, "pom.xml")) {
			fmt.Fprintln(w, "Compiling Java project with Maven…")
			c := exec.Command("mvn", "-q", "compile")
			c.Dir = dir
			c.Stdout = w
			c.Stderr = os.Stderr
			return c.Run()
		} else if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
			fmt.Fprintln(w, "Compiling Java project with Gradle…")
			c := exec.Command("gradle", "-q", "classes")
			c.Dir = dir
			c.Stdout = w
			c.Stderr = os.Stderr
			return c.Run()
		}
		return fmt.Errorf("java runtime: no pom.xml or build.gradle found in %s", dir)

	case "yaml":
		// No dependencies to install for YAML projects.
		return nil
	}
	return nil
}

// ensurePythonDependencies creates a venv and installs packages from
// requirements.txt. Uses `uv` when available for faster installs, otherwise
// falls back to the standard `python3 -m venv` + `pip` workflow.
func ensurePythonDependencies(dir, venv string) error {
	return ensurePythonDependenciesTo(dir, venv, os.Stdout)
}

// ensurePythonDependenciesTo is the writer-aware version of ensurePythonDependencies.
func ensurePythonDependenciesTo(dir, venv string, w io.Writer) error {
	venvDir := pythonVenvDir(venv)
	venvPath := filepath.Join(dir, venvDir)

	if hasUV() {
		return ensurePythonDependenciesUV(dir, venvPath, w)
	}
	return ensurePythonDependenciesPip(dir, venvPath, w)
}

// ensurePythonDependenciesUV uses `uv` to create the venv and install packages.
func ensurePythonDependenciesUV(dir, venvPath string, w io.Writer) error {
	fmt.Fprintf(w, "Creating venv at %s (using uv)…\n", venvPath)
	c := exec.Command("uv", "venv", venvPath)
	c.Dir = dir
	c.Stdout = w
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("creating venv with uv: %w", err)
	}

	// Prefer pyproject.toml, fall back to requirements.txt for legacy projects.
	pyprojectFile := filepath.Join(dir, "pyproject.toml")
	reqFile := filepath.Join(dir, "requirements.txt")

	uvEnv := append(os.Environ(), "VIRTUAL_ENV="+venvPath)

	switch {
	case fileExists(pyprojectFile):
		fmt.Fprintln(w, "Installing Python dependencies from pyproject.toml (using uv)…")
		// Non-package mode projects (e.g. Poetry with package-mode = false) cannot
		// be installed with `pip install -e .` because there is nothing to build.
		// In that case, extract the dependency list and install packages directly.
		if isNonPackageMode(pyprojectFile) {
			deps := parsePyprojectDeps(pyprojectFile)
			if len(deps) == 0 {
				return nil // nothing to install
			}
			args := append([]string{"pip", "install", "-q"}, deps...)
			c = exec.Command("uv", args...)
		} else {
			c = exec.Command("uv", "pip", "install", "-e", ".", "-q")
		}
		c.Dir = dir
		c.Env = uvEnv
		c.Stdout = w
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("installing dependencies from pyproject.toml with uv: %w", err)
		}
	case fileExists(reqFile):
		fmt.Fprintln(w, "Installing Python dependencies (using uv)…")
		c = exec.Command("uv", "pip", "install", "-r", "requirements.txt", "-q")
		c.Dir = dir
		// Set VIRTUAL_ENV so uv pip knows which environment to install into.
		c.Env = uvEnv
		c.Stdout = w
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("installing dependencies with uv: %w", err)
		}
	}
	return nil
}

// ensurePythonDependenciesPip uses the standard library venv + pip workflow.
func ensurePythonDependenciesPip(dir, venvPath string, w io.Writer) error {
	fmt.Fprintf(w, "Creating venv at %s…\n", venvPath)
	c := exec.Command("python3", "-m", "venv", venvPath)
	c.Dir = dir
	c.Stdout = w
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("creating venv: %w", err)
	}

	venvPython := filepath.Join(venvPath, "bin", "python3")

	// Prefer pyproject.toml, fall back to requirements.txt for legacy projects.
	pyprojectFile := filepath.Join(dir, "pyproject.toml")
	reqFile := filepath.Join(dir, "requirements.txt")

	switch {
	case fileExists(pyprojectFile):
		fmt.Fprintln(w, "Installing Python dependencies from pyproject.toml…")
		// Non-package mode projects cannot be installed with `pip install -e .`.
		// Extract the dependency list and install packages directly instead.
		if isNonPackageMode(pyprojectFile) {
			deps := parsePyprojectDeps(pyprojectFile)
			if len(deps) == 0 {
				return nil
			}
			args := append([]string{"-m", "pip", "install", "-q"}, deps...)
			c = exec.Command(venvPython, args...)
		} else {
			c = exec.Command(venvPython, "-m", "pip", "install", "-e", ".", "-q")
		}
		c.Dir = dir
		c.Stdout = w
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("installing dependencies from pyproject.toml with pip: %w", err)
		}
	case fileExists(reqFile):
		fmt.Fprintln(w, "Installing Python dependencies…")
		c = exec.Command(venvPython, "-m", "pip", "install", "-r", "requirements.txt", "-q")
		c.Dir = dir
		c.Stdout = w
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("installing dependencies with pip: %w", err)
		}
	}
	return nil
}

// isNonPackageMode reports whether a pyproject.toml declares a non-installable
// project. This covers:
//   - Poetry:  [tool.poetry] package-mode = false
//   - PEP 621: no [project] table at all (bare tool-only config)
var reNonPackageMode = regexp.MustCompile(`(?m)^\s*package-mode\s*=\s*false`)

func isNonPackageMode(pyprojectPath string) bool {
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return false
	}
	return reNonPackageMode.Match(data)
}

// parsePyprojectDeps extracts dependency specifiers from pyproject.toml without
// a full TOML parser. It handles two common layouts:
//
//  1. PEP 621 / uv:  [project] dependencies = ["pkg>=1.0", ...]
//  2. Poetry:        [tool.poetry.dependencies] pkg = ">=1.0"
//
// Python version constraints (key == "python") are skipped.
// Returns a slice of pip-compatible specifiers, e.g. ["pulumi==3.228.0", "pulumi-aws==7.23.0"].
func parsePyprojectDeps(pyprojectPath string) []string {
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return nil
	}
	content := string(data)

	// Try PEP 621 [project] dependencies array first.
	if deps := parsePEP621Deps(content); len(deps) > 0 {
		return deps
	}
	// Fall back to Poetry [tool.poetry.dependencies] table.
	return parsePoetryDeps(content)
}

// parsePEP621Deps extracts deps from a [project] dependencies = [...] array.
var rePEP621Section = regexp.MustCompile(`(?ms)^\[project\].*?^dependencies\s*=\s*\[([^\]]*)\]`)
var reQuotedDep = regexp.MustCompile(`["']([^"']+)["']`)

func parsePEP621Deps(content string) []string {
	m := rePEP621Section.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	var deps []string
	for _, dm := range reQuotedDep.FindAllStringSubmatch(m[1], -1) {
		dep := strings.TrimSpace(dm[1])
		if dep != "" {
			deps = append(deps, dep)
		}
	}
	return deps
}

// parsePoetryDeps extracts deps from [tool.poetry.dependencies] key = "version" pairs.
// It reads lines between the section header and the next [section] header.
var rePoetryDepLine = regexp.MustCompile(`^(\S+)\s*=\s*["']([^"']+)["']`)

func parsePoetryDeps(content string) []string {
	lines := strings.Split(content, "\n")
	inSection := false
	var deps []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[tool.poetry.dependencies]" {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break // next section
			}
			m := rePoetryDepLine.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			name, ver := m[1], m[2]
			if strings.EqualFold(name, "python") {
				continue // skip python version constraint
			}
			// Convert Poetry version specifier to pip specifier.
			// Poetry uses "^1.0" (caret) and "~1.0" (tilde) which pip doesn't understand.
			// Map them to >= equivalents for installation purposes.
			ver = poetryVerToPip(name, ver)
			if ver != "" {
				deps = append(deps, ver)
			} else {
				deps = append(deps, name)
			}
		}
	}
	return deps
}

// poetryVerToPip converts a Poetry version specifier to a pip-compatible one.
// e.g. "^3.228.0" → "pulumi>=3.228.0", "==3.228.0" → "pulumi==3.228.0"
func poetryVerToPip(name, ver string) string {
	ver = strings.TrimSpace(ver)
	switch {
	case strings.HasPrefix(ver, "^"):
		// Caret: compatible release from this version onward (major pinned).
		return name + ">=" + ver[1:]
	case strings.HasPrefix(ver, "~"):
		// Tilde: compatible release (minor pinned).
		return name + "~=" + ver[1:]
	case ver == "*":
		return name // no constraint
	default:
		// Already a pip specifier: ==, >=, <=, !=, >
		return name + ver
	}
}

// fileExists reports whether the named file exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// resolveNodeEntry finds the TypeScript/JavaScript entry point for a Node.js Pulumi project.
// It checks package.json "main" first, then falls back to index.ts / index.js.
func resolveNodeEntry(dir string) (string, error) {
	// Try to read main from package.json
	pkgData, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err == nil {
		var pkg struct {
			Main string `json:"main"`
		}
		if jsonErr := json.Unmarshal(pkgData, &pkg); jsonErr == nil && pkg.Main != "" {
			// If main points to a .js file, check for a sibling .ts file first
			// (the .ts is the source; .js would only exist after tsc).
			mainPath := filepath.Join(dir, pkg.Main)
			tsVariant := strings.TrimSuffix(mainPath, ".js") + ".ts"
			if _, err := os.Stat(tsVariant); err == nil {
				return tsVariant, nil
			}
			if _, err := os.Stat(mainPath); err == nil {
				return mainPath, nil
			}
		}
	}
	// Fall back to index.ts, then index.js
	for _, candidate := range []string{"index.ts", "index.js"} {
		p := filepath.Join(dir, candidate)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find a Node.js entry point in %s (tried package.json main, index.ts, index.js)", dir)
}

// prependPath returns a copy of env with the given directory prepended to PATH.
func prependPath(env []string, dir string) []string {
	const prefix = "PATH="
	result := make([]string, len(env))
	copy(result, env)
	for i, e := range result {
		if strings.HasPrefix(e, prefix) {
			result[i] = prefix + dir + string(os.PathListSeparator) + e[len(prefix):]
			return result
		}
	}
	// PATH not found in env — add it
	result = append(result, prefix+dir)
	return result
}

// prependPythonPath returns a copy of env with the given directory prepended to PYTHONPATH.
func prependPythonPath(env []string, dir string) []string {
	const prefix = "PYTHONPATH="
	result := make([]string, len(env))
	copy(result, env)
	for i, e := range result {
		if strings.HasPrefix(e, prefix) {
			result[i] = prefix + dir + string(os.PathListSeparator) + e[len(prefix):]
			return result
		}
	}
	result = append(result, prefix+dir)
	return result
}

// reMissingConfig matches Pulumi's "Missing required configuration variable 'ns:key'" message.
var reMissingConfig = regexp.MustCompile(`Missing required configuration variable '([^']+)'`)

// parseMissingConfigKeys extracts config key names from Pulumi stderr output.
func parseMissingConfigKeys(stderr string) []string {
	matches := reMissingConfig.FindAllStringSubmatch(stderr, -1)
	seen := map[string]bool{}
	var keys []string
	for _, m := range matches {
		key := m[1]
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// Static source scanning for required config keys
// ---------------------------------------------------------------------------

// Regex patterns for extracting required config keys from source code.
// Python:     config.require("key")  /  config.require_secret("key")
// TypeScript: config.require("key")  /  config.requireSecret("key")
// Go:         cfg.Require("key")     /  cfg.RequireSecret("key")
var reConfigRequire = regexp.MustCompile(
	`(?:\.require|\.require_secret|\.requireSecret|\.Require|\.RequireSecret)\(\s*["']([^"']+)["']`,
)

// rePulumiConfig matches `pulumi.Config("namespace")` or `new pulumi.Config("namespace")`
// or `Config("namespace")` to detect the config namespace.
var rePulumiConfigNS = regexp.MustCompile(
	`(?:pulumi\.)?Config\(\s*["']([^"']+)["']\s*\)`,
)

// scanRequiredConfigKeys scans source files in the project directory for
// config.require(...) / config.require_secret(...) calls and returns the
// fully-qualified config key names (namespace:key).
func scanRequiredConfigKeys(dir, projectName, runtime string) []string {
	// Determine which file extensions to scan based on runtime.
	var globs []string
	switch runtime {
	case "python":
		globs = []string{"*.py"}
	case "nodejs", "node":
		globs = []string{"*.ts", "*.js"}
	case "go":
		globs = []string{"*.go"}
	case "dotnet":
		globs = []string{"*.cs", "*.fs"}
	case "java":
		globs = []string{"*.java"}
	case "yaml":
		// YAML projects declare resources declaratively; no config.require() calls to scan.
		return nil
	default:
		globs = []string{"*.py", "*.ts", "*.js", "*.go", "*.cs", "*.java"}
	}

	seen := map[string]bool{}
	var keys []string

	for _, glob := range globs {
		matches, err := filepath.Glob(filepath.Join(dir, glob))
		if err != nil {
			continue
		}
		for _, fpath := range matches {
			data, err := os.ReadFile(fpath)
			if err != nil {
				continue
			}
			content := string(data)
			fileKeys := extractRequiredKeys(content, projectName)
			for _, k := range fileKeys {
				if !seen[k] {
					seen[k] = true
					keys = append(keys, k)
				}
			}
		}
	}

	sort.Strings(keys)
	return keys
}

// extractRequiredKeys parses source code content and returns fully-qualified
// config keys (namespace:key). It tracks which Config object uses which
// namespace via simple line-by-line analysis.
func extractRequiredKeys(content, defaultNS string) []string {
	lines := strings.Split(content, "\n")

	// Map variable names to their config namespace.
	// e.g. "config" -> "voting-app", "aws_config" -> "aws"
	varNS := map[string]string{}

	// Patterns to detect config variable assignments:
	// Python: config = pulumi.Config()  or  config = pulumi.Config("ns")
	// TS:     const config = new pulumi.Config()  or  new pulumi.Config("ns")
	// Go:     cfg := config.New(ctx, "ns")
	reAssignDefault := regexp.MustCompile(`(\w+)\s*[:=]\s*(?:new\s+)?(?:pulumi\.)?Config\(\s*\)`)
	reAssignNS := regexp.MustCompile(`(\w+)\s*[:=]\s*(?:new\s+)?(?:pulumi\.)?Config\(\s*["']([^"']+)["']\s*\)`)
	// Go style: cfg, err := config.New(ctx, "ns")  or  cfg := config.New(ctx, "ns")
	reGoAssign := regexp.MustCompile(`(\w+)(?:\s*,\s*\w+)?\s*[:=]\s*(?:\w+\.)?(?:New|Try)\w*\(\s*\w+\s*,\s*["']([^"']+)["']\s*\)`)

	var keys []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect config variable assignments
		if m := reAssignNS.FindStringSubmatch(trimmed); m != nil {
			varNS[m[1]] = m[2]
		} else if m := reAssignDefault.FindStringSubmatch(trimmed); m != nil {
			varNS[m[1]] = defaultNS
		} else if m := reGoAssign.FindStringSubmatch(trimmed); m != nil {
			varNS[m[1]] = m[2]
		}

		// Detect require calls
		// Match patterns like: config.require("key"), config.require_secret("key"), etc.
		reCall := regexp.MustCompile(`(\w+)\.(require|require_secret|requireSecret|Require|RequireSecret)\(\s*["']([^"']+)["']`)
		if m := reCall.FindStringSubmatch(trimmed); m != nil {
			varName := m[1]
			configKey := m[3]

			ns := defaultNS
			if mappedNS, ok := varNS[varName]; ok {
				ns = mappedNS
			}

			fullKey := ns + ":" + configKey
			keys = append(keys, fullKey)
		}
	}

	return keys
}

// pricingRelevantSuffixes are config key suffixes that affect cost estimation.
// Keys matching these should NOT be auto-filled with dummy values.
var pricingRelevantSuffixes = []string{
	":region",
	":location",
	":zone",
	":instanceType",
	":instance_type",
	":instanceClass",
	":instance_class",
	":nodeType",
	":node_type",
	":machineType",
	":machine_type",
	":vmSize",
	":vm_size",
	":tier",
	":sku",
}

// pricingRelevantExact are full config keys that affect cost estimation.
var pricingRelevantExact = map[string]bool{
	"aws:region":            true,
	"aws:config:region":     true,
	"azure-native:location": true,
	"azure:location":        true,
	"gcp:region":            true,
	"gcp:zone":              true,
	"oci:region":            true,
}

// isPricingRelevantConfigKey returns true if the config key affects pricing
// and should NOT be auto-filled with a dummy value.
func isPricingRelevantConfigKey(key string) bool {
	if pricingRelevantExact[key] {
		return true
	}
	lower := strings.ToLower(key)
	for _, suffix := range pricingRelevantSuffixes {
		if strings.HasSuffix(lower, strings.ToLower(suffix)) {
			return true
		}
	}
	return false
}

// dummyValueForConfigKey returns a reasonable dummy value for a non-pricing
// config key. For keys that look like they expect a number, returns "1".
// For keys that look like secrets/passwords, returns a long-enough string.
// For keys that look like names/usernames, returns "dummy".
// Otherwise returns "dummy".
func dummyValueForConfigKey(key string) string {
	lower := strings.ToLower(key)

	// Numeric-looking keys
	numericHints := []string{"size", "count", "number", "num", "port", "desired", "min", "max", "replicas", "capacity"}
	for _, hint := range numericHints {
		if strings.Contains(lower, hint) {
			return "1"
		}
	}

	// Secret/password/key-looking keys — some programs validate minimum length
	secretHints := []string{"password", "secret", "token", "key", "credential"}
	for _, hint := range secretHints {
		if strings.Contains(lower, hint) {
			return "dummySecret01234567890123456789012345678901234567890"
		}
	}

	// Name/username-looking keys
	nameHints := []string{"name", "user", "email", "admin", "owner", "account"}
	for _, hint := range nameHints {
		if strings.Contains(lower, hint) {
			return "dummy"
		}
	}

	return "dummy"
}

// parseUsageFlags parses --usage name=quantity flags into a map.
// Each entry must be in the form "resource-name=number", e.g. "my-api=5000000".
// Invalid entries are silently skipped.
func parseUsageFlags(flags []string) map[string]float64 {
	if len(flags) == 0 {
		return nil
	}
	m := make(map[string]float64, len(flags))
	for _, kv := range flags {
		idx := strings.LastIndexByte(kv, '=')
		if idx < 1 {
			continue
		}
		name := strings.TrimSpace(kv[:idx])
		valStr := strings.TrimSpace(kv[idx+1:])
		if name == "" || valStr == "" {
			continue
		}
		var qty float64
		if _, err := fmt.Sscanf(valStr, "%f", &qty); err != nil || qty <= 0 {
			continue
		}
		m[name] = qty
	}
	return m
}

// parseModelFlags parses --model flags into a map of resource name → ModelSelector.
// Format: [resource-name=]Model[:PurchaseOption[:Term]]
//
// Examples:
//
//	--model "Reserved:standard:1yr"              → global selector (key "")
//	--model "my-ec2=Reserved:standard:1yr"       → resource-specific
//	--model "spot"                               → global spot
//	--model "ComputeSavingsPlans:No Upfront:3yr" → global savings plan
func parseModelFlags(flags []string) map[string]estimate.ModelSelector {
	if len(flags) == 0 {
		return nil
	}
	m := make(map[string]estimate.ModelSelector, len(flags))
	for _, flag := range flags {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			continue
		}

		// Split off optional resource-name prefix: "name=Model:..."
		resourceName := ""
		spec := flag
		if idx := strings.IndexByte(flag, '='); idx > 0 {
			resourceName = strings.TrimSpace(flag[:idx])
			spec = strings.TrimSpace(flag[idx+1:])
		}

		// Parse "Model[:PurchaseOption[:Term]]"
		parts := strings.SplitN(spec, ":", 3)
		sel := estimate.ModelSelector{
			Model: strings.TrimSpace(parts[0]),
		}
		if len(parts) >= 2 {
			sel.PurchaseOption = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			sel.Term = strings.TrimSpace(parts[2])
		}
		if sel.Model == "" {
			continue
		}
		m[resourceName] = sel
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
