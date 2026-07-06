package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestParseUseArgs(t *testing.T) {
	cases := []struct {
		name string
		use  string
		want []schemaArg
	}{
		{"no args", "sleep", []schemaArg{}},
		{"one required arg", "get <id>", []schemaArg{{Name: "id", Required: true}}},
		{"one optional arg", "sleep [document_id]", []schemaArg{{Name: "document_id", Required: false}}},
		{
			"mixed required and optional",
			"cmd <required> [optional]",
			[]schemaArg{{Name: "required", Required: true}, {Name: "optional", Required: false}},
		},
		{
			"two positionals with a subcommand placeholder ignored",
			"schema [command]",
			[]schemaArg{{Name: "command", Required: false}},
		},
		{"bare tokens without brackets are not args", "cmd foo bar", []schemaArg{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUseArgs(tc.use)
			if len(got) != len(tc.want) {
				t.Fatalf("parseUseArgs(%q) = %v, want %v", tc.use, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseUseArgs(%q)[%d] = %+v, want %+v", tc.use, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseExitCodes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int
	}{
		{"empty defaults to ok+internal", "", []int{0, 1}},
		{"single code", "0", []int{0}},
		{"multiple codes", "0,2,3,5,6,7,8", []int{0, 2, 3, 5, 6, 7, 8}},
		{"tolerates surrounding whitespace", "0, 2 , 3", []int{0, 2, 3}},
		{"skips unparseable entries", "0,x,3", []int{0, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseExitCodes(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseExitCodes(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseExitCodes(%q)[%d] = %d, want %d", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// buildSyntheticTree returns a small cobra command tree exercising flags,
// required/optional args, annotations, and a nested subcommand, standing in
// for the real command registrations without depending on any of them.
func buildSyntheticTree() *cobra.Command {
	root := &cobra.Command{
		Use:   "root",
		Short: "root short",
	}
	root.PersistentFlags().Bool("verbose", false, "be verbose")

	child := &cobra.Command{
		Use:   "child <id> [optional]",
		Short: "child short",
		Long:  "child long description",
		Annotations: map[string]string{
			annStdout:    "json",
			annExitCodes: "0,2,3",
		},
	}
	child.Flags().String("fields", "", "comma-separated fields")
	child.Flags().Int("limit", 10, "max results")

	hidden := &cobra.Command{Use: "hidden", Short: "should not appear", Hidden: true}
	helpCmd := &cobra.Command{Use: "help", Short: "help command should be filtered"}

	root.AddCommand(child, hidden, helpCmd)
	return root
}

func TestDescribeCommand(t *testing.T) {
	root := buildSyntheticTree()
	var child *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "child" {
			child = c
		}
	}
	if child == nil {
		t.Fatal("synthetic tree missing the child command")
	}

	sc := describeCommand(child)

	if sc.Name != "child" {
		t.Errorf("Name = %q, want %q", sc.Name, "child")
	}
	if sc.Short != "child short" {
		t.Errorf("Short = %q, want %q", sc.Short, "child short")
	}
	if sc.Long != "child long description" {
		t.Errorf("Long = %q, want %q", sc.Long, "child long description")
	}
	if sc.Stdout != "json" {
		t.Errorf("Stdout = %q, want %q", sc.Stdout, "json")
	}
	wantExit := []int{0, 2, 3}
	if len(sc.ExitCodes) != len(wantExit) {
		t.Fatalf("ExitCodes = %v, want %v", sc.ExitCodes, wantExit)
	}
	for i, c := range wantExit {
		if sc.ExitCodes[i] != c {
			t.Errorf("ExitCodes[%d] = %d, want %d", i, sc.ExitCodes[i], c)
		}
	}

	wantArgs := []schemaArg{{Name: "id", Required: true}, {Name: "optional", Required: false}}
	if len(sc.Args) != len(wantArgs) {
		t.Fatalf("Args = %v, want %v", sc.Args, wantArgs)
	}
	for i := range wantArgs {
		if sc.Args[i] != wantArgs[i] {
			t.Errorf("Args[%d] = %+v, want %+v", i, sc.Args[i], wantArgs[i])
		}
	}

	flagNames := map[string]schemaFlag{}
	for _, f := range sc.Flags {
		flagNames[f.Name] = f
	}
	if _, ok := flagNames["fields"]; !ok {
		t.Error("expected a \"fields\" flag in the schema")
	}
	if f, ok := flagNames["limit"]; !ok || f.Default != "10" {
		t.Errorf("expected a \"limit\" flag with default \"10\", got %+v (present=%v)", f, ok)
	}
	if _, ok := flagNames["help"]; ok {
		t.Error("the built-in \"help\" flag must be filtered out")
	}

	if len(sc.Commands) != 0 {
		t.Errorf("leaf command should have no nested Commands, got %v", sc.Commands)
	}
}

func TestDescribeChildrenFiltersHiddenAndHelp(t *testing.T) {
	root := buildSyntheticTree()
	children := describeChildren(root)

	names := map[string]bool{}
	for _, c := range children {
		names[c.Name] = true
	}
	if !names["child"] {
		t.Error("expected \"child\" among described children")
	}
	if names["hidden"] {
		t.Error("hidden commands must not appear in the schema")
	}
	if names["help"] {
		t.Error("the auto-generated \"help\" command must be filtered out")
	}
}

func TestDescribeCommandRecursesIntoChildren(t *testing.T) {
	root := buildSyntheticTree()
	sc := describeCommand(root)
	found := false
	for _, c := range sc.Commands {
		if c.Name == "child" {
			found = true
		}
	}
	if !found {
		t.Errorf("describeCommand(root).Commands should include \"child\", got %v", sc.Commands)
	}
}

func TestFindCommand(t *testing.T) {
	root := buildSyntheticTree()
	if c := findCommand(root, "child"); c == nil || c.Name() != "child" {
		t.Errorf("findCommand(root, \"child\") = %v, want the child command", c)
	}
	if c := findCommand(root, "does-not-exist"); c != nil {
		t.Errorf("findCommand for an unknown name should return nil, got %v", c)
	}
}

func TestDescribeFlagSetOmitsHelpAndVersion(t *testing.T) {
	root := buildSyntheticTree()
	root.Flags().Bool("help", false, "help for root")
	root.Flags().Bool("version", false, "version for root")
	root.Flags().String("real", "x", "a real flag")

	flags := describeFlagSet(root.Flags())
	for _, f := range flags {
		if f.Name == "help" || f.Name == "version" {
			t.Errorf("describeFlagSet should omit built-in %q flag", f.Name)
		}
	}
	found := false
	for _, f := range flags {
		if f.Name == "real" {
			found = true
		}
	}
	if !found {
		t.Error("describeFlagSet should include ordinary flags")
	}
}
