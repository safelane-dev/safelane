// Command safelane coordinates evidence-shaped releases through Argo Rollouts.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/cli"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/project"
	githubverify "github.com/AndrewMaged814/safelane/internal/verify/github"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
	"github.com/spf13/cobra"
)

// version is overridden at build time via:
//
//	go build -ldflags "-X main.version=$(git rev-parse --short HEAD)"
var version = "dev"

type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

type commandRuntime struct {
	root     string
	app      string
	stdout   io.Writer
	stderr   io.Writer
	storeDir string
	project  string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	app, args, err := extractGlobalApp(args)
	if err != nil {
		fmt.Fprintf(stderr, "safelane: %v\n", err)
		return cli.ExitUsage
	}
	rt := resolveCommandRuntime(".", app, stdout, stderr)
	restoreCaller := activateDemoCaller(rt.project)
	defer restoreCaller()
	root := newRootCommand(rt)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var coded exitError
		if errors.As(err, &coded) {
			return coded.code
		}
		fmt.Fprintf(stderr, "safelane: %v\n", err)
		return cli.ExitUsage
	}
	return cli.ExitOK
}

func activateDemoCaller(projectFile string) func() {
	if projectFile == "" {
		return func() {}
	}
	cfg, err := project.Load(projectFile)
	if err != nil || cfg.Application != "safelane-demo-api" {
		return func() {}
	}
	caller := filepath.Join(filepath.Dir(projectFile), "caller.kubeconfig")
	if _, err := os.Stat(caller); err != nil {
		return func() {}
	}
	previous, existed := os.LookupEnv("KUBECONFIG")
	_ = os.Setenv("KUBECONFIG", caller)
	return func() {
		if existed {
			_ = os.Setenv("KUBECONFIG", previous)
		} else {
			_ = os.Unsetenv("KUBECONFIG")
		}
	}
}

func resolveCommandRuntime(root, app string, stdout, stderr io.Writer) commandRuntime {
	rt := commandRuntime{root: root, app: app, stdout: stdout, stderr: stderr}
	if app != "" {
		if home, err := project.Home(); err == nil {
			loc := project.ForApp(home, app)
			rt.storeDir, rt.project = loc.ReleasesDir, loc.ProjectFile
		}
		return rt
	}
	if loc, err := project.Resolve(root); err == nil {
		rt.storeDir, rt.project = loc.ReleasesDir, loc.ProjectFile
	} else if home, homeErr := project.Home(); homeErr == nil {
		rt.storeDir = filepath.Join(home, "apps", ".unmatched", "releases")
	}
	return rt
}

func extractGlobalApp(args []string) (string, []string, error) {
	clean := make([]string, 0, len(args))
	app := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--app":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", nil, fmt.Errorf("--app requires a value")
			}
			app, i = args[i+1], i+1
		case strings.HasPrefix(a, "--app="):
			app = strings.TrimPrefix(a, "--app=")
		default:
			clean = append(clean, a)
		}
	}
	if app != "" && project.SanitizeApplication(app) != app {
		return "", nil, fmt.Errorf("--app %q must be a lowercase DNS label", app)
	}
	return app, clean, nil
}

func newRootCommand(rt commandRuntime) *cobra.Command {
	root := &cobra.Command{
		Use:           "safelane",
		Short:         "Risk-shaped release coordination for coding agents",
		Long:          "SafeLane turns code evidence into a bounded rollout and coordinates Argo through promotion or rollback.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(rt.stdout)
	root.SetErr(rt.stderr)
	root.PersistentFlags().String("app", rt.app, "select an application outside its repository")
	root.AddCommand(setupGroup(rt), legacyLeaf(rt, "doctor [--json]", "Check whether SafeLane can release right now", cli.DoctorCommand(rt.root), injectProject))
	root.AddCommand(releaseGroup(rt), completionCommand(root), versionCommand())
	root.AddCommand(discoverCommand(rt), registerCommand(rt), inspectReleaseCommand(rt), recommendCommand(rt))
	return root
}

// recommendCommand validates the active session's assessment against the frozen
// evidence and prints the recommendation. SafeLane produces no assessment of
// its own; this is where somebody else's arrives and gets checked.
func recommendCommand(rt commandRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recommend <env> <assessment-json-path|->",
		Short: "Validate an assessment and give the recommendation",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			forceJSON, _ := cmd.Flags().GetBool("json")
			home, err := config.Home()
			if err != nil {
				return err
			}
			code := cli.Recommend(cmd.Context(), cli.RecommendOptions{
				Inspect: cli.InspectOptions{
					Root:        rt.root,
					Home:        home,
					Environment: args[0],
					App:         rt.app,
					ForceJSON:   forceJSON,
					Cluster:     discovery.Service{},
					Source:      &githubverify.Client{Token: os.Getenv("GITHUB_TOKEN")},
					Registry:    oci.Resolver{Registry: oci.Remote{}},
				},
				AssessmentPath: args[1],
				Stdin:          os.Stdin,
			}, rt.stdout, rt.stderr)
			if code != cli.ExitOK {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "force machine output at a terminal")
	return cmd
}

// inspectReleaseCommand freezes the evidence boundary for one release and
// reports its four views. The Environment is the positional argument and the
// revision is an optional second one, per decision 11's rules 2 and 6.
func inspectReleaseCommand(rt commandRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect <env> [<revision>]",
		Short: "Freeze the evidence for a release and show what it is",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			forceJSON, _ := cmd.Flags().GetBool("json")
			home, err := config.Home()
			if err != nil {
				return err
			}
			revision := ""
			if len(args) > 1 {
				revision = args[1]
			}
			code := cli.Inspect(cmd.Context(), cli.InspectOptions{
				Root:        rt.root,
				Home:        home,
				Environment: args[0],
				Revision:    revision,
				App:         rt.app,
				ForceJSON:   forceJSON,
				Cluster:     discovery.Service{},
				Source:      &githubverify.Client{Token: os.Getenv("GITHUB_TOKEN")},
				Registry:    oci.Resolver{Registry: oci.Remote{}},
			}, rt.stdout, rt.stderr)
			if code != cli.ExitOK {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "force machine output at a terminal")
	return cmd
}

// discoverCommand and registerCommand are the first two commands on the new
// surface (decision 11): no `setup` prefix, the positional argument is the
// thing the user actually says, and output shape follows where it is going
// rather than a flag.
func discoverCommand(rt commandRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover <namespace>",
		Short: "Read one namespace and report what SafeLane could release",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			forceJSON, _ := cmd.Flags().GetBool("json")
			code := cli.Discover(cmd.Context(), cli.DiscoverOptions{
				Root:      rt.root,
				Namespace: args[0],
				ForceJSON: forceJSON,
				Service:   discovery.Service{},
			}, rt.stdout, rt.stderr)
			if code != cli.ExitOK {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "force machine output at a terminal")
	return cmd
}

func registerCommand(rt commandRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register <selection-json-path|->",
		Short: "Confirm a discovered selection and write the configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			forceJSON, _ := cmd.Flags().GetBool("json")
			home, err := config.Home()
			if err != nil {
				return err
			}
			code := cli.Register(cmd.Context(), cli.RegisterOptions{
				Root:          rt.root,
				Home:          home,
				SelectionPath: args[0],
				App:           rt.app,
				ForceJSON:     forceJSON,
				Service:       discovery.Service{},
				Stdin:         os.Stdin,
			}, rt.stdout, rt.stderr)
			if code != cli.ExitOK {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "force machine output at a terminal")
	return cmd
}

type argInjector func(commandRuntime, []string) []string

func injectProject(rt commandRuntime, args []string) []string {
	if rt.project == "" {
		return args
	}
	return append([]string{"--project", rt.project}, args...)
}

func injectProjectAndStore(rt commandRuntime, args []string) []string {
	if rt.project != "" {
		args = append([]string{"--project", rt.project}, args...)
	}
	if rt.storeDir != "" {
		args = append([]string{"--store-dir", rt.storeDir}, args...)
	}
	return args
}

func injectStore(rt commandRuntime, args []string) []string {
	if rt.storeDir == "" {
		return args
	}
	return append([]string{"--store-dir", rt.storeDir}, args...)
}

func legacyLeaf(rt commandRuntime, use, short string, command cli.Command, inject argInjector, prefix ...string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if arg == "-h" || arg == "--help" {
					return cmd.Help()
				}
			}
			callArgs := append(append([]string(nil), prefix...), inject(rt, args)...)
			if code := command.Run(cmd.Context(), callArgs, rt.stdout, rt.stderr); code != cli.ExitOK {
				return exitError{code: code}
			}
			return nil
		},
	}
}

func releaseGroup(rt commandRuntime) *cobra.Command {
	releaseCmd := &cobra.Command{Use: "release", Short: "Plan, run, and prove a release", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	releaseCmd.AddCommand(
		legacyLeaf(rt, "plan --pr <number> [--repo <owner/name>] [--environment <name>] [--json]", "Compile and persist a Safety Contract without mutating production", cli.ReleasePlanCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "run <release-id> [--yes] [--step] [--timeout 20m] [--json]", "Coordinate an approved release to a terminal outcome", cli.ReleaseRunCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "status [release-id] [--json]", "Reconcile and show release state", cli.ReleaseStatusCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "proof <release-id> [--details | --json]", "Show durable release proof", cli.ReleaseProofCommand(rt.storeDir), injectStore),
		legacyLeaf(rt, "retry <release-id> [--json]", "Create a new attempt after re-verifying evidence", cli.ReleaseRetryCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "accept-risk <release-id> --hazard <id> --reason <reason> [--yes] [--json]", "Accept one explicitly identified uncovered hazard", cli.ReleaseAcceptRiskCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "pause <release-id> --reason <reason> [--yes] [--json]", "Emergency-pause a release", cli.ReleasePauseCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "resume <release-id> --reason <reason> [--yes] [--json]", "Resume an explicitly emergency-paused release", cli.ReleaseResumeCommand(rt.root, rt.storeDir), injectProjectAndStore),
		legacyLeaf(rt, "abort <release-id> --reason <reason> [--yes] [--json]", "Emergency-abort a release", cli.ReleaseAbortCommand(rt.root, rt.storeDir), injectProjectAndStore),
	)
	return releaseCmd
}

func setupGroup(rt commandRuntime) *cobra.Command {
	setup := legacyLeaf(rt, "setup [--yes] [--json]", "Create operator-owned configuration from repository facts", cli.SetupCommand(rt.root), func(_ commandRuntime, args []string) []string { return args })
	setup.AddCommand(
		legacyLeaf(rt, "inspect [--json]", "Inspect repository facts and persist their fingerprint", cli.SetupInspectCommand(rt.root), func(_ commandRuntime, args []string) []string { return args }),
		legacyLeaf(rt, "plan --findings <absolute-path|-> [--json]", "Compile agent findings into an immutable setup plan", cli.SetupPlanCommand(rt.root), func(_ commandRuntime, args []string) []string { return args }),
		legacyLeaf(rt, "apply <setup-id> [--yes] [--json]", "Apply one immutable setup plan", cli.SetupApplyCommand(rt.root), func(_ commandRuntime, args []string) []string { return args }),
	)
	return setup
}

func completionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate a shell completion script",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return exitError{code: cli.ExitUsage}
			}
		},
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the SafeLane build version", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintln(cmd.OutOrStdout(), version)
	}}
}
