package cli

import (
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// Completion of flag VALUES, which is the half cobra does not do for us: it
// completes flag names and generates the shell scripts, but only the command
// knows what a --session or a --dataTypes may be.
//
// Everything here is offline. Completion runs on every tab press, so it must
// never make a network call - which is also why --rootId is not completed:
// resolving device names means querying the tenant, and a shell that pauses for
// an authenticated round trip is worse than one that completes nothing.

// registerCompletions attaches value completion to the flags that have a
// knowable set of values.
func registerCompletions(cmd *cobra.Command) {
	for _, name := range []string{"session", "sourceSession", "targetSession"} {
		if cmd.Flag(name) == nil {
			continue
		}
		_ = cmd.RegisterFlagCompletionFunc(name, completeSession)
	}
	if cmd.Flag("dataTypes") != nil {
		_ = cmd.RegisterFlagCompletionFunc("dataTypes", completeDataTypes)
	}
	if cmd.Flag("childTypes") != nil {
		_ = cmd.RegisterFlagCompletionFunc("childTypes", completeChildTypes)
	}
}

// completeSession offers the session files go-c8y-cli wrote.
//
// Names, not paths: that is what --session takes, and it is what the operator
// recognises. The session home is read directly, as everywhere else in this
// tool, so completion works without the c8y binary.
func completeSession(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	home, _ := cmd.Flags().GetString("sessionHome")
	entries, err := os.ReadDir(session.Home(home))
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), toComplete) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	// No file completion as a fallback: offering the working directory's files
	// where a session name belongs is noise.
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeDataTypes(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeCommaList(toComplete,
		[]string{
			string(export.DataMeasurements),
			string(export.DataEvents),
			string(export.DataAlarms),
		},
		// "all" only makes sense on its own, so it is offered only when nothing
		// has been chosen yet.
		string(export.DataAll))
}

func completeChildTypes(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeCommaList(toComplete, []string{
		string(export.ChildAsset),
		string(export.ChildDevice),
		string(export.ChildAddition),
	})
}

// completeCommaList completes one element of a comma separated value, keeping
// what was already typed and leaving out what was already chosen.
//
// exclusive values are offered only when the list is still empty, which is what
// makes "all" behave: it cannot be combined with anything, so suggesting it
// after a comma would suggest something the command refuses.
func completeCommaList(toComplete string, values []string, exclusive ...string) ([]string, cobra.ShellCompDirective) {
	prefix, last := "", toComplete
	if index := strings.LastIndex(toComplete, ","); index >= 0 {
		prefix, last = toComplete[:index+1], toComplete[index+1:]
	}

	chosen := map[string]bool{}
	for _, already := range strings.Split(prefix, ",") {
		if already != "" {
			chosen[already] = true
		}
	}

	candidates := values
	if prefix == "" {
		candidates = append(append([]string{}, values...), exclusive...)
	}

	var completions []string
	for _, value := range candidates {
		if chosen[value] || !strings.HasPrefix(value, last) {
			continue
		}
		completions = append(completions, prefix+value)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}
