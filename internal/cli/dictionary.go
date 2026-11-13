package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

func newDictionaryCommand(serviceURL *string, version string) *cobra.Command {
	command := &cobra.Command{
		Use:   "dictionary",
		Short: "Manage the words your workspace accepts in identifiers",
		Long: `Manage your workspace dictionary: the identifier words that checks in the
workspace accept, so misspelled-identifier findings stop reporting them.

The dictionary belongs to the workspace, not to one repository.`,
		Args: cobra.NoArgs,
	}
	command.AddCommand(
		newDictionaryAddCommand(serviceURL, version),
		newDictionaryListCommand(serviceURL, version),
		newDictionaryRemoveCommand(serviceURL, version),
	)
	return command
}

func newDictionaryAddCommand(serviceURL *string, version string) *cobra.Command {
	workspace, word := "", ""
	command := &cobra.Command{
		Use:   "add <finding-id> | --word <word>",
		Short: "Add a misspelled-identifier finding's word, or a word, to the dictionary",
		Long: `Add a word to your workspace dictionary.

With a finding id, the one a check printed for a misspelled-identifier
finding, it adds the finding's suggested word. With --word it adds that word:
one identifier part of ASCII letters, such as parsr, HTTP or Kubectl. Later
checks in the workspace stop reporting the word as misspelled. Adding a word
twice is fine.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			findingID := ""
			if len(args) == 1 {
				findingID = args[0]
			}
			return runDictionaryAdd(cmd, *serviceURL, version, workspace, findingID, word)
		},
	}
	command.Flags().StringVar(&word, "word", "", "word to add, instead of a finding's suggested word")
	addWorkspaceFlag(command, false, &workspace)
	return command
}

func runDictionaryAdd(cmd *cobra.Command, serviceFlag, version, workspace, findingID, word string) error {
	findingID, word = strings.TrimSpace(findingID), strings.TrimSpace(word)
	request := service.DictionaryAddRequest{}
	switch {
	case findingID != "" && word != "":
		return errors.New("invalid argument: pass a finding id or --word, not both")
	case findingID != "":
		if !uuidPattern.MatchString(findingID) {
			return fmt.Errorf("finding id %q is not a UUID; use the id a check printed for the finding", findingID)
		}
		request.FindingID = findingID
	case word != "":
		if err := checkDictionaryWord(word); err != nil {
			return err
		}
		request.Word = word
	default:
		return errors.New("invalid argument: pass the finding id a check printed, or --word <word>")
	}
	client, err := workspaceClient(cmd, serviceFlag, version, workspace)
	if err != nil {
		return err
	}
	response, err := client.AddDictionaryWord(cmd.Context(), request)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	forgetWord(".", findingID, response.Word)
	added := "the finding's word"
	if response.Word != "" {
		added = fmt.Sprintf("%q", report.Printable(response.Word))
	}
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"slopcop: added %s to the workspace dictionary; later checks will not report it as misspelled\n",
		added,
	)
	return nil
}

func newDictionaryListCommand(serviceURL *string, version string) *cobra.Command {
	workspace, format := "", ""
	command := &cobra.Command{
		Use:   "list",
		Short: "List the words in your workspace dictionary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := workspaceClient(cmd, *serviceURL, version, workspace)
			if err != nil {
				return err
			}
			listed, err := client.Dictionary(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), listed)
			}
			writeDictionary(cmd.OutOrStdout(), listed)
			return nil
		},
	}
	addFormatFlag(command, &format)
	addWorkspaceFlag(command, false, &workspace)
	return command
}

// writeDictionary prints the workspace dictionary for a person or an agent.
func writeDictionary(out io.Writer, listed service.DictionaryList) {
	switch len(listed.Words) {
	case 0:
		fmt.Fprintln(out, "slopcop: the workspace dictionary is empty")
		return
	case 1:
		fmt.Fprintln(out, "slopcop: 1 word in the workspace dictionary")
	default:
		fmt.Fprintf(out, "slopcop: %d words in the workspace dictionary\n", len(listed.Words))
	}
	for _, word := range listed.Words {
		fmt.Fprintf(out, "  %s\n", dictionaryLine(word))
	}
	fmt.Fprintln(out, "slopcop: remove one with `slopcop dictionary remove <word>`")
}

// dictionaryLine is one word, then when, by whom, and from which finding it
// was added.
func dictionaryLine(word service.DictionaryWord) string {
	line := fmt.Sprintf("%s (added %s by %s", report.Printable(word.Word), ignoredWhen(word.CreatedAt), ignoredBy(word.AddedBy))
	if word.SourceFindingID != nil && *word.SourceFindingID != "" {
		line += " from finding " + report.Printable(*word.SourceFindingID)
	}
	return line + ")"
}

func newDictionaryRemoveCommand(serviceURL *string, version string) *cobra.Command {
	workspace := ""
	command := &cobra.Command{
		Use:   "remove <word>",
		Short: "Remove a word from your workspace dictionary",
		Long: `Remove a word from your workspace dictionary, so later checks in the workspace
report it as misspelled again. Removing a word the dictionary does not hold is
fine. Only the workspace's owners and managers may remove words.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			word := strings.TrimSpace(args[0])
			if err := checkDictionaryWord(word); err != nil {
				return err
			}
			client, err := workspaceClient(cmd, *serviceURL, version, workspace)
			if err != nil {
				return err
			}
			if err := client.RemoveDictionaryWord(cmd.Context(), word); err != nil {
				return serviceFailure(cmd, err)
			}
			forgetCheck(".")
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"slopcop: removed %q from the workspace dictionary; later checks report it as misspelled again\n",
				word,
			)
			return nil
		},
	}
	addWorkspaceFlag(command, false, &workspace)
	return command
}

// checkDictionaryWord refuses a word the dictionary would not take, before
// it reaches the service.
func checkDictionaryWord(word string) error {
	if !service.ValidDictionaryWord(word) {
		return fmt.Errorf(
			"invalid argument %q: a dictionary word is one identifier part of ASCII letters (lower case, UPPER case or Capitalized), at most %d letters",
			report.Printable(word), service.MaxDictionaryWordBytes,
		)
	}
	return nil
}
