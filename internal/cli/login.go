package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

func newLoginCommand(serviceURL *string, version string) *cobra.Command {
	var token string
	makeDefault := false
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to the SlopCop check service",
		Long: `Sign in to the SlopCop check service.

With no flags, login starts the browser device flow: it prints a link that
opens the approval page with the code filled in, then waits for approval. Pass --token to store an existing credential,
which is what CI and agent harnesses use.

Each service keeps its own login. --service or $SLOPCOP_SERVICE signs in to
another deployment and leaves the other logins as they are. When it is the
first login on this machine, that service also becomes the default, since no
command could use the default's missing login; otherwise the default stays,
and --default also makes it the default for every command, which asks at a
terminal like service use does.

Run slopcop init once to install the agent skills and user-level hooks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			target := strings.TrimRight(resolveServiceURL(cfg, *serviceURL), "/")
			credential := strings.TrimSpace(token)
			// The check a command makes of its service URL, before a
			// credential is stored for one no command could use.
			client, err := service.New(target, "", "slopcop/"+version, 0)
			if err != nil {
				return err
			}
			hosted := config.Origin(target) == config.DefaultServiceURL
			if makeDefault && config.Origin(target) != config.Origin(cfg.DefaultService()) {
				// The same step as service use, so the same question, and
				// before anything is sent or stored.
				if err := confirmDefaultService(cmd, cfg, target, "login --default"); err != nil {
					return err
				}
			}
			if credential == "" {
				if !hosted {
					fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: signing in to %s\n", config.Origin(target))
				}
				noticeUpdates(cmd, client, version)
				start, err := client.StartDevice(cmd.Context())
				if err != nil {
					return remedyError(err)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), start.Instructions())
				credential, err = client.AwaitDeviceToken(cmd.Context(), start, func(line string) {
					fmt.Fprintln(cmd.ErrOrStderr(), line)
				})
				if err != nil {
					return remedyError(err)
				}
			}
			previous := config.Origin(cfg.DefaultService())
			// A first login moves the default to its service: every other
			// command would otherwise go to a service with no login. The
			// question service use asks guards logins the machine already
			// has, and there are none.
			firstLogin := config.Origin(target) != previous && onlyLoginWouldBe(cfg, target)
			cfg.SetToken(target, credential)
			if makeDefault || firstLogin {
				cfg.ServiceURL = target
			}
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: logged in to %s (%s)\n", target, path)
			if current := config.Origin(cfg.DefaultService()); current != previous && !makeDefault {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"slopcop: %s is now the default service, as this machine has no other login; `slopcop service use %s` moves it back\n",
					current, previous)
			} else if current != config.Origin(target) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"slopcop: the default service is still %s; pass --service %s to use this login, or make it the default with `slopcop service use %s`\n",
					current, target, target)
			}
			return nil
		},
	}
	command.Flags().StringVar(&token, "token", "", "store an existing token instead of starting the browser flow")
	command.Flags().BoolVar(&makeDefault, "default", false, "also make this service the default for every command; asks at a terminal")
	return command
}

// onlyLoginWouldBe reports whether a login for target would be the only one
// the configuration holds: none is stored for any other service.
func onlyLoginWouldBe(cfg config.Config, target string) bool {
	origin := config.Origin(target)
	for _, signedIn := range cfg.SignedIn() {
		if signedIn != origin {
			return false
		}
	}
	return true
}

// remedyError prints a service failure the way a check does: the closed
// message plus the remedy.
func remedyError(err error) error {
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		return errors.New(remedyText(serviceError))
	}
	return err
}

// loginHints name the slopcop command for remedies that are actually to sign
// in. The service's remedies do not name a client, so the client adds its own.
// Repo-state failures (repo_not_indexed, no_access) keep the service's remedy
// alone: those say "sign in again" only as a step after installing the App or
// getting access, and appending a login command sends an already-signed-in
// user back through the login flow for a problem login cannot fix.
//
// The account commands' failures get the command that lists what to choose
// from in the same way. A disabled repository gets whoami: "your workspace"
// is the one this login is bound to, which may not be the one the person
// expects.
var loginHints = map[string]string{
	"auth_required":                  "Sign in with `slopcop login`.",
	"auth_expired":                   "Sign in with `slopcop login`.",
	"account_required":               "Sign in with `slopcop login`.",
	"device_flow_disabled":           "Sign in with `slopcop login --token <token>`.",
	"workspace_not_found":            "List them with `slopcop workspace list`.",
	"workspace_repository_not_found": "List them with `slopcop repo list`.",
	"owner_required":                 "Name a workspace you own with `--workspace <name>`.",
	"repository_disabled":            "`slopcop whoami` shows which workspace this login checks in.",
}

// remedyText is the service's message and remedy, verbatim, plus the slopcop
// command when the remedy is to sign in.
func remedyText(serviceError *service.Error) string {
	text := serviceError.RemedyText()
	if hint := loginHints[serviceError.Body.Code]; hint != "" {
		text += "\n" + hint
	}
	if separateShellLogins() && shellLoginCodes[serviceError.Body.Code] {
		text += "\n" + windowsShellHint()
	}
	return text
}

// separateShellLogins reports whether this machine is Windows, where shells
// can each keep their own login: a Windows binary, or the Linux one in WSL,
// which has a home of its own apart from the Windows user's. It is a
// variable so tests can stand in for either.
var separateShellLogins = func() bool {
	return runtime.GOOS == "windows" || os.Getenv("WSL_DISTRO_NAME") != ""
}

// shellLoginCodes are the refusals a login made in another shell explains:
// no login reached the service, or one that cannot see a repository the
// person already added, such as a login made before the App was installed.
var shellLoginCodes = map[string]bool{
	"auth_required":      true,
	"auth_expired":       true,
	"account_required":   true,
	"repo_not_indexed":   true,
	"app_not_installed":  true,
	"app_no_repo_access": true,
	"no_access":          true,
}

// windowsShellHint says that on Windows a login does not carry over between
// shells. PowerShell, cmd, Git Bash and WSL can each resolve a different
// config directory, so an agent's terminal may not see the login the person
// made in their own, or may hold an older one, and the refusal then reads as
// a repository that was never added. It names the file this shell reads so
// the person can compare it with their own terminal's.
func windowsShellHint() string {
	where := "its own config file"
	if path, err := config.Path(); err == nil {
		where = path
	}
	return "On Windows, each shell (PowerShell, cmd, Git Bash, WSL) can keep its own slopcop login; this one reads " +
		where + ". If you signed in or installed the SlopCop App from another terminal, run `slopcop login` " +
		"in the terminal the agent uses, then check again."
}
