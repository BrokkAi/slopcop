package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

func newLoginCommand(serviceURL *string, version string) *cobra.Command {
	var token string
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to the SlopCop check service",
		Long: `Sign in to the SlopCop check service.

With no flags, login starts the browser device flow: it prints a URL and a
code, then waits for approval. Pass --token to store an existing credential,
which is what CI and agent harnesses use.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			target, _ := resolveService(cfg, *serviceURL)
			target = strings.TrimRight(target, "/")
			credential := strings.TrimSpace(token)
			if credential == "" {
				client, err := service.New(target, "", "slopcop/"+version, 0)
				if err != nil {
					return err
				}
				start, err := client.StartDevice(cmd.Context())
				if err != nil {
					return remedyError(err)
				}
				fmt.Fprintf(
					cmd.ErrOrStderr(),
					"Open %s and enter code %s\n",
					start.VerificationURI, start.UserCode,
				)
				credential, err = client.AwaitDeviceToken(cmd.Context(), start, func(line string) {
					fmt.Fprintln(cmd.ErrOrStderr(), line)
				})
				if err != nil {
					return remedyError(err)
				}
			}
			cfg.ServiceURL = target
			cfg.Token = credential
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: logged in to %s (%s)\n", target, path)
			return nil
		},
	}
	command.Flags().StringVar(&token, "token", "", "store an existing token instead of starting the browser flow")
	return command
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
// alone: repo_not_indexed's names slopcop login only as a step after
// installing the App, and appending a login command sends an already-signed-in
// user back through the login flow for a problem login cannot fix.
var loginHints = map[string]string{
	service.CodeAuthRequired:       "Sign in with `slopcop login`.",
	service.CodeAuthExpired:        "Sign in with `slopcop login`.",
	service.CodeDeviceFlowDisabled: "Sign in with `slopcop login --token <token>`.",
}

// remedyText is the service's message and remedy, verbatim, plus the slopcop
// command when the remedy is to sign in.
func remedyText(serviceError *service.Error) string {
	text := serviceError.RemedyText()
	if hint := loginHints[serviceError.Body.Code]; hint != "" {
		text += "\n" + hint
	}
	return text
}
