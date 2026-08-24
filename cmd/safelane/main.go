// Command safelane coordinates an Application Release Candidate to an
// Environment through Argo Rollouts.
//
// The command surface is decision 11's, and it is short on purpose:
//
//	safelane discover <namespace>
//	safelane register <selection-json-path|->
//	safelane inspect <env> [<revision>]
//	safelane recommend <env> <assessment-json-path|->
//	safelane run <env>
//	safelane status <env>
//	safelane hold <env> <reason>
//	safelane continue <env> <reason>
//	safelane stop <env> <reason>
//	safelane proof <env> [--details]
//
// There is no `release` prefix, because SafeLane is a release tool and the
// noun is implied. There is no `--json` on the agent path: output is JSON when
// stdout is not a terminal and readable text when it is, so the same command
// serves a script and a person. There is no `--yes`, because a flag the caller
// passes every time is not a safety mechanism - at a terminal `run` asks, and
// piped, the agent records the user's direct answer through a hidden adapter
// before run. A recommendation is advice, never authorization.
//
// The Environment is always the positional argument, because it is the one
// thing the user actually says. No command takes a release identifier: there
// is one active release per Application and Environment, and a person who has
// to look up an identifier to stop a rollout is a person who will not stop it
// in time.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/cli"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/journal"
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
	root   string
	app    string
	stdout io.Writer
	stderr io.Writer
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	app, args, err := extractGlobalApp(args)
	if err != nil {
		fmt.Fprintf(stderr, "safelane: %v\n", err)
		return cli.ExitUsage
	}
	root := newRootCommand(commandRuntime{root: ".", app: app, stdout: stdout, stderr: stderr})
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

// extractGlobalApp pulls `--app` out before cobra sees it, so it works the
// same in front of every command.
//
// It is the only flag that identifies anything, and it exists for exactly one
// case: a checkout registered as more than one Application. Its absence is
// never an error when the inference is unique.
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
	return app, clean, nil
}

func newRootCommand(rt commandRuntime) *cobra.Command {
	root := &cobra.Command{
		Use:           "safelane",
		Short:         "Release coordination for coding agents",
		Long:          "SafeLane reads what changed, recommends how far to release it, and stays attached to an existing Argo Rollout through a terminal outcome.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(rt.stdout)
	root.SetErr(rt.stderr)
	root.PersistentFlags().String("app", rt.app, "name the application when this repository is registered as more than one")

	root.AddCommand(discoverCommand(rt), registerCommand(rt), inspectCommand(rt),
		recommendCommand(rt), approveCommand(rt), confirmBaselineCommand(rt), confirmBuildCommand(rt), evidenceCommand(rt), registerApplyCommand(rt), runCommand(rt))
	root.AddCommand(naturalControls(rt)...)
	root.AddCommand(completionCommand(root), versionCommand())
	return root
}

// evidenceCommand is an agent adapter for loading a content-addressed source
// diff only when the assessment needs more detail than the four compact views.
func evidenceCommand(rt commandRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "evidence <env> <handle>",
		Short:  "Load evidence named by a frozen Release Delta",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := config.Home()
			if err != nil {
				return err
			}
			return exit(cli.Evidence(cmd.Context(), cli.EvidenceOptions{
				Root: rt.root, Home: home, Application: rt.app, Environment: args[0], HandleID: args[1],
				Origin: discovery.GitHubOrigin, Source: &githubverify.Client{Token: os.Getenv("GITHUB_TOKEN")},
			}, rt.stdout, rt.stderr))
		},
	}
	return cmd
}

// confirmBuildCommand is the agent adapter used after the user selects one of
// several successful exact-revision workflows listed by inspection.
func confirmBuildCommand(rt commandRuntime) *cobra.Command {
	cmd := withJSON(&cobra.Command{
		Use:    "confirm-build <env> <run-id>",
		Short:  "Record which successful workflow produced this release's container",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exit(cli.ConfirmBuild(cmd.Context(), cli.ConfirmBuildOptions{
				Inspect: rt.readers(args[0], "", jsonFlag(cmd)), RunID: args[1],
			}, rt.stdout, rt.stderr))
		},
	})
	return cmd
}

// confirmBaselineCommand is the agent adapter for the one-time adoption case
// where the running image has no usable source metadata. The active session
// first asks the user which exact commit is deployed.
func confirmBaselineCommand(rt commandRuntime) *cobra.Command {
	cmd := withJSON(&cobra.Command{
		Use:    "confirm-baseline <env> <revision>",
		Short:  "Bind the running image to the exact commit the user confirmed",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := config.Home()
			if err != nil {
				return err
			}
			source := &githubverify.Client{Token: os.Getenv("GITHUB_TOKEN")}
			return exit(cli.ConfirmBaseline(cmd.Context(), cli.ConfirmBaselineOptions{
				Root: rt.root, Home: home, Environment: args[0], Revision: args[1], App: rt.app,
				ForceJSON: jsonFlag(cmd), Cluster: discovery.Service{},
				Registry: oci.Resolver{Registry: oci.Remote{}}, Checker: source,
			}, rt.stdout, rt.stderr))
		},
	})
	return cmd
}

// approveCommand is hidden because it is an adapter for the active agent
// session, not another concept the user must learn. The agent passes the
// user's exact answer after showing the final approval question.
func approveCommand(rt commandRuntime) *cobra.Command {
	cmd := withJSON(&cobra.Command{
		Use:    "approve <env> <answer>",
		Short:  "Record the user's answer to the final rollout question",
		Hidden: true,
		Args:   cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := config.Home()
			if err != nil {
				return err
			}
			return exit(cli.Approve(cmd.Context(), cli.ApproveOptions{
				Root: rt.root, Home: home, Environment: args[0], App: rt.app,
				Answer: strings.Join(args[1:], " "), ForceJSON: jsonFlag(cmd),
				Origin: discovery.GitHubOrigin,
			}, rt.stdout, rt.stderr))
		},
	})
	return cmd
}

// readers builds the three production ports. They are values on the options
// struct rather than globals, so a test substitutes any of them without a
// build tag.
func (rt commandRuntime) readers(environment, revision string, forceJSON bool) cli.InspectOptions {
	home, _ := config.Home()
	return cli.InspectOptions{
		Root:        rt.root,
		Home:        home,
		Environment: environment,
		Revision:    revision,
		App:         rt.app,
		ForceJSON:   forceJSON,
		Cluster:     discovery.Service{},
		Source:      &githubverify.Client{Token: os.Getenv("GITHUB_TOKEN")},
		Registry:    oci.Resolver{Registry: oci.Remote{}},
		History: func(application, environment string) ([]delta.HistoryCard, error) {
			dir := config.ForApp(home, application).ForEnvironment(environment).Dir
			cards, err := (journal.Store{Dir: dir}).History(delta.HistoryLimit)
			if err != nil {
				return nil, err
			}
			result := make([]delta.HistoryCard, 0, len(cards))
			for _, card := range cards {
				result = append(result, delta.HistoryCard{
					At: card.At, Revision: card.Candidate, Outcome: card.Outcome,
					Lane: card.Lane, Note: delta.Untrusted(card.Reason),
				})
			}
			return result, nil
		},
	}
}

func jsonFlag(cmd *cobra.Command) bool {
	forceJSON, _ := cmd.Flags().GetBool("json")
	return forceJSON
}

func withJSON(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().Bool("json", false, "force machine output at a terminal")
	return cmd
}

func discoverCommand(rt commandRuntime) *cobra.Command {
	return withJSON(&cobra.Command{
		Use:   "discover <namespace>",
		Short: "Read one namespace and report what SafeLane could release",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exit(cli.Discover(cmd.Context(), cli.DiscoverOptions{
				Root:      rt.root,
				Namespace: args[0],
				ForceJSON: jsonFlag(cmd),
				Service:   discovery.Service{},
			}, rt.stdout, rt.stderr))
		},
	})
}

func registerCommand(rt commandRuntime) *cobra.Command {
	return withJSON(&cobra.Command{
		Use:   "register <selection-json-path|->",
		Short: "Confirm a discovered selection and write the configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := config.Home()
			if err != nil {
				return err
			}
			return exit(cli.Register(cmd.Context(), cli.RegisterOptions{
				Root:          rt.root,
				Home:          home,
				SelectionPath: args[0],
				App:           rt.app,
				ForceJSON:     jsonFlag(cmd),
				Service:       discovery.Service{},
				Stdin:         os.Stdin,
				Confirm:       os.Stdin,
			}, rt.stdout, rt.stderr))
		},
	})
}

// registerApplyCommand is the agent adapter used only after the person has
// seen the complete preview produced by register and approved that exact file.
func registerApplyCommand(rt commandRuntime) *cobra.Command {
	cmd := withJSON(&cobra.Command{
		Use:    "register-apply <selection-json-path|->",
		Short:  "Write a registration that the user already approved",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := config.Home()
			if err != nil {
				return err
			}
			return exit(cli.Register(cmd.Context(), cli.RegisterOptions{
				Root: rt.root, Home: home, SelectionPath: args[0], App: rt.app,
				ForceJSON: jsonFlag(cmd), Service: discovery.Service{}, Stdin: os.Stdin,
				Apply: true,
			}, rt.stdout, rt.stderr))
		},
	})
	return cmd
}

// inspectCommand freezes the evidence boundary for one release.
//
// The revision is a second optional positional rather than a flag:
// `safelane inspect production a1b2c3d` releases that exact commit, and
// because it is validated against the registered repository's default-branch
// history, a bare value cannot be mistaken for anything else.
func inspectCommand(rt commandRuntime) *cobra.Command {
	return withJSON(&cobra.Command{
		Use:   "inspect <env> [<revision>]",
		Short: "Freeze the evidence for a release and show what it is",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			revision := ""
			if len(args) > 1 {
				revision = args[1]
			}
			return exit(cli.Inspect(cmd.Context(),
				rt.readers(args[0], revision, jsonFlag(cmd)), rt.stdout, rt.stderr))
		},
	})
}

// recommendCommand validates the active session's assessment. SafeLane
// produces no assessment of its own; this is where somebody else's arrives and
// gets checked for grounding.
func recommendCommand(rt commandRuntime) *cobra.Command {
	return withJSON(&cobra.Command{
		Use:   "recommend <env> <assessment-json-path|->",
		Short: "Validate an assessment and give the recommendation",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exit(cli.Recommend(cmd.Context(), cli.RecommendOptions{
				Inspect:        rt.readers(args[0], "", jsonFlag(cmd)),
				AssessmentPath: args[1],
				Stdin:          os.Stdin,
			}, rt.stdout, rt.stderr))
		},
	})
}

func runCommand(rt commandRuntime) *cobra.Command {
	return withJSON(&cobra.Command{
		Use:   "run <env>",
		Short: "Release the recommendation awaiting your approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exit(cli.Run(cmd.Context(), cli.RunOptions{
				Inspect: rt.readers(args[0], "", jsonFlag(cmd)),
				Confirm: os.Stdin,
			}, rt.stdout, rt.stderr))
		},
	})
}

// naturalControls are the five commands that address a release in progress.
// The reason on hold, continue and stop is positional text, because a control
// with no recorded reason is not useful in proof.
func naturalControls(rt commandRuntime) []*cobra.Command {
	options := func(cmd *cobra.Command, args []string) cli.ControlOptions {
		details, _ := cmd.Flags().GetBool("details")
		home, _ := config.Home()
		reason := ""
		if len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		return cli.ControlOptions{
			Root: rt.root, Home: home, Environment: args[0],
			App: rt.app, ForceJSON: jsonFlag(cmd), Reason: reason, Details: details,
		}
	}
	leaf := func(use, short string, positional cobra.PositionalArgs,
		invoke func(context.Context, cli.ControlOptions, io.Writer, io.Writer) int) *cobra.Command {

		return withJSON(&cobra.Command{
			Use: use, Short: short, Args: positional,
			RunE: func(cmd *cobra.Command, args []string) error {
				return exit(invoke(cmd.Context(), options(cmd, args), rt.stdout, rt.stderr))
			},
		})
	}

	proof := leaf("proof <env>", "Show what happened", cobra.ExactArgs(1), cli.Proof)
	// --details stays opt-in: loading full proof by default would spend an
	// agent's context on records nobody asked for.
	proof.Flags().Bool("details", false, "open the full record for this release")

	return []*cobra.Command{
		leaf("status <env>", "Say where the release is and what it is waiting for", cobra.ExactArgs(1), cli.Status),
		leaf("hold <env> <reason>", "Hold the rollout where it is", cobra.MinimumNArgs(2), cli.Hold),
		leaf("continue <env> <reason>", "Continue a held rollout", cobra.MinimumNArgs(2), cli.Continue),
		leaf("stop <env> <reason>", "Stop the rollout and restore the stable version", cobra.MinimumNArgs(2), cli.Stop),
		proof,
	}
}

func exit(code int) error {
	if code != cli.ExitOK {
		return exitError{code: code}
	}
	return nil
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
	return &cobra.Command{Use: "version", Short: "Print the SafeLane build version", Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		}}
}
