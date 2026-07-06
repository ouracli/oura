package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ouracli/oura/internal/envelope"
	"github.com/ouracli/oura/internal/ouraapi"
	"github.com/ouracli/oura/internal/output"
)

// newDataCmds builds one cobra command per entry in ouraapi.Endpoints. The
// registry is the single source of truth: this generation loop, the MCP tool
// list, and the schema manifest all walk the same slice so the three surfaces
// cannot drift from one another.
func newDataCmds() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(ouraapi.Endpoints))
	for _, ep := range ouraapi.Endpoints {
		ep := ep // capture for the closures below
		if ep.Style == ouraapi.StyleSingleObject {
			cmds = append(cmds, newObjectCmd(ep))
			continue
		}
		cmds = append(cmds, newListCmd(ep))
	}
	return cmds
}

// dataCmdOpts holds the flag values for one generated data command. Not every
// field applies to every Style; each command only registers the flags its
// endpoint actually supports.
type dataCmdOpts struct {
	start     string
	end       string
	latest    bool
	nextToken string
	all       bool
	fields    string
}

// newListCmd builds the command for a collection-style endpoint (every Style
// except StyleSingleObject): a paginated List(), an optional single-document
// fetch, and an --all NDJSON stream.
func newListCmd(ep ouraapi.Endpoint) *cobra.Command {
	var opts dataCmdOpts

	use := ep.CLI
	if ep.HasDocID {
		use = fmt.Sprintf("%s [document_id]", ep.CLI)
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: ep.Short,
		Annotations: map[string]string{
			// --all switches stdout from a single JSON document to an NDJSON
			// stream; annotations are static strings, so both shapes are
			// documented here rather than picked at runtime. The full data-path
			// exit-code set: 1 (a stdout/NDJSON write failure), 4 (a keyring or
			// credential-file load problem), plus the auth/usage/api/network/
			// ratelimit/subscription codes the fetch itself can produce.
			annStdout:    "json|ndjson(--all)",
			annExitCodes: "0,1,2,3,4,5,6,7,8",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListCmd(cmd, ep, &opts, args)
		},
	}
	if ep.HasDocID {
		cmd.Args = cobra.MaximumNArgs(1)
	} else {
		cmd.Args = cobra.NoArgs
	}
	if ep.Deprecated {
		cmd.Deprecated = "use 'oura tags' (enhanced tags)"
	}

	f := cmd.Flags()
	switch ep.Style {
	case ouraapi.StyleDateRange:
		f.StringVar(&opts.start, "start", "", "start date, YYYY-MM-DD (default: 7 days before --end)")
		f.StringVar(&opts.end, "end", "", "end date, YYYY-MM-DD (default: today)")
	case ouraapi.StyleDatetimeRange:
		f.StringVar(&opts.start, "start", "", "start datetime, RFC3339 or YYYY-MM-DD (default: 7 days before --end); a plain date normalizes to 00:00:00 local")
		f.StringVar(&opts.end, "end", "", "end datetime, RFC3339 or YYYY-MM-DD (default: now); a plain date normalizes to 23:59:59 local")
		f.BoolVar(&opts.latest, "latest", false, "request only the most recent sample, ignoring --start/--end")
	}
	f.StringVar(&opts.nextToken, "next-token", "", "resume pagination from this token")
	f.BoolVar(&opts.all, "all", false,
		"follow next_token and stream every document to stdout as NDJSON (one document per line), "+
			"terminated by a {\"type\":\"summary\"} line, instead of printing a single page")
	if ep.HasFields {
		f.StringVar(&opts.fields, "fields", "", "comma-separated field projection to request from Oura")
	}
	return cmd
}

// runListCmd is the shared RunE body for every collection-style endpoint.
func runListCmd(cmd *cobra.Command, ep ouraapi.Endpoint, opts *dataCmdOpts, args []string) error {
	ctx := cmd.Context()
	if globals.sandbox && !ep.Sandbox {
		return noSandboxRouteErr(ep)
	}

	// A positional document_id short-circuits straight to the single-document
	// route, ignoring the range/pagination flags.
	if ep.HasDocID && len(args) == 1 {
		client, err := apiClient(ctx)
		if err != nil {
			return err
		}
		raw, err := client.Doc(ctx, ep, args[0], opts.fields)
		if err != nil {
			return err
		}
		return printResult(raw)
	}

	// Build and validate the query (parsing the date flags) BEFORE resolving
	// credentials, so a malformed --start/--end is reported as a usage error
	// regardless of whether the caller has usable credentials.
	q := url.Values{}
	switch ep.Style {
	case ouraapi.StyleDateRange:
		start, end, err := resolveDateRange(opts.start, opts.end)
		if err != nil {
			return err
		}
		q.Set("start_date", start)
		q.Set("end_date", end)
	case ouraapi.StyleDatetimeRange:
		if opts.latest {
			q.Set("latest", "true")
		} else {
			start, end, err := resolveDatetimeRange(opts.start, opts.end)
			if err != nil {
				return err
			}
			q.Set("start_datetime", start)
			q.Set("end_datetime", end)
		}
	}
	if ep.HasFields && opts.fields != "" {
		q.Set("fields", opts.fields)
	}
	if opts.nextToken != "" {
		q.Set("next_token", opts.nextToken)
	}

	client, err := apiClient(ctx)
	if err != nil {
		return err
	}

	if opts.all {
		return streamAll(ctx, client, ep, q)
	}
	lr, err := client.List(ctx, ep, q)
	if err != nil {
		return err
	}
	return printResult(lr)
}

// newObjectCmd builds the command for a StyleSingleObject endpoint
// (personal_info): no flags, no pagination, just the bare document.
func newObjectCmd(ep ouraapi.Endpoint) *cobra.Command {
	cmd := &cobra.Command{
		Use:   ep.CLI,
		Short: ep.Short,
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			annStdout: "json",
			// 1 (stdout write failure) and 4 (credential-file/keyring load
			// problem) are reachable alongside the auth/usage/api/network/
			// ratelimit/subscription codes from the fetch itself.
			annExitCodes: "0,1,2,3,4,5,6,7,8",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if globals.sandbox && !ep.Sandbox {
				return noSandboxRouteErr(ep)
			}
			client, err := apiClient(ctx)
			if err != nil {
				return err
			}
			raw, err := client.Object(ctx, ep)
			if err != nil {
				return err
			}
			return printResult(raw)
		},
	}
	if ep.Deprecated {
		cmd.Deprecated = "use 'oura tags' (enhanced tags)"
	}
	return cmd
}

// noSandboxRouteErr reports that ep has no sandbox equivalent (only
// personal_info, at present) — driven off the registry's Sandbox field so it
// can never drift as endpoints are added.
func noSandboxRouteErr(ep ouraapi.Endpoint) error {
	return envelope.New(envelope.KindUsage, "no_sandbox_route",
		fmt.Sprintf("%s has no sandbox route", ep.CLI),
		fmt.Sprintf("%s has no sandbox equivalent; use real credentials", ep.CLI))
}

// resolveDateRange parses --start/--end as YYYY-MM-DD, defaulting --end to
// today and --start to 7 days before --end, computed from time.Now in the
// user's local zone. It returns both re-formatted as YYYY-MM-DD for the
// API's start_date/end_date params.
func resolveDateRange(startFlag, endFlag string) (start, end string, err error) {
	endT := time.Now()
	if endFlag != "" {
		endT, err = time.Parse("2006-01-02", endFlag)
		if err != nil {
			return "", "", envelope.New(envelope.KindUsage, "bad_end_date",
				fmt.Sprintf("invalid --end %q: %v", endFlag, err),
				"dates must be YYYY-MM-DD, e.g. 2026-07-06")
		}
	}
	startT := endT.AddDate(0, 0, -7)
	if startFlag != "" {
		startT, err = time.Parse("2006-01-02", startFlag)
		if err != nil {
			return "", "", envelope.New(envelope.KindUsage, "bad_start_date",
				fmt.Sprintf("invalid --start %q: %v", startFlag, err),
				"dates must be YYYY-MM-DD, e.g. 2026-06-29")
		}
	}
	return startT.Format("2006-01-02"), endT.Format("2006-01-02"), nil
}

// resolveDatetimeRange parses --start/--end as RFC3339 or a plain date,
// defaulting --end to now and --start to 7 days before --end. Both are
// returned formatted as RFC3339 for the API's start_datetime/end_datetime
// params.
func resolveDatetimeRange(startFlag, endFlag string) (start, end string, err error) {
	endT := time.Now()
	if endFlag != "" {
		endT, err = normalizeDatetimeFlag(endFlag, true)
		if err != nil {
			return "", "", err
		}
	}
	startT := endT.AddDate(0, 0, -7)
	if startFlag != "" {
		startT, err = normalizeDatetimeFlag(startFlag, false)
		if err != nil {
			return "", "", err
		}
	}
	return startT.Format(time.RFC3339), endT.Format(time.RFC3339), nil
}

// normalizeDatetimeFlag accepts either an RFC3339 timestamp or a plain
// YYYY-MM-DD date. A plain date normalizes to local midnight (start of day),
// or to 23:59:59 local (end of day) when endOfDay is true.
func normalizeDatetimeFlag(raw string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if d, err := time.Parse("2006-01-02", raw); err == nil {
		if endOfDay {
			return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local), nil
		}
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local), nil
	}
	return time.Time{}, envelope.New(envelope.KindUsage, "bad_datetime",
		fmt.Sprintf("invalid datetime %q", raw),
		"use RFC3339 (e.g. 2026-07-06T00:00:00Z) or a plain date YYYY-MM-DD")
}

// streamAll follows next_token pagination for ep via ouraapi's ListAll
// helper, emitting one raw document per NDJSON line and always terminating
// with a {"type":"summary"} line. A pagination failure partway through is
// folded into the summary (ok:false plus an "error" envelope) rather than
// written as a competing stdout artifact: once the NDJSON stream has started,
// stdout has already committed to that shape for this invocation. The failure
// is ALSO returned as a stderrOnlyError so the process still exits with the
// error's mapped code — the exit-code enum is the contract agents branch on,
// and folding the error into the summary must not silently downgrade it to 0.
func streamAll(ctx context.Context, client *ouraapi.Client, ep ouraapi.Endpoint, q url.Values) error {
	nd := output.NewNDJSON(os.Stdout)
	listErr := client.ListAll(ctx, ep, q, func(doc json.RawMessage) error {
		return nd.Emit(doc)
	})
	extra := map[string]any{"endpoint": ep.CLI}
	if listErr != nil {
		extra["ok"] = false
		extra["error"] = envelope.From(listErr)
	}
	if err := nd.Summary(extra); err != nil {
		return envelope.New(envelope.KindInternal, "ndjson_summary_write", err.Error(), "")
	}
	if listErr != nil {
		// The error is already recorded in the summary line on stdout; only set
		// the exit code (and print the human summary to stderr) via main().
		return &stderrOnlyError{env: envelope.From(listErr)}
	}
	return nil
}
