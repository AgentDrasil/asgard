// Command subagent starts and manages subagents: child chats of the calling
// agent's chat that run other agents of its team. It talks to the runner's
// callback socket, which stamps the identity of the caller; an agent can only
// see and act on its own subagents.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// stateQueued is the state of a writable subagent that waits for the workspace.
const stateQueued = "queued"

const usageText = `Usage: subagent <command> [options]

Commands:
  spawn --agent <name> [--model <m>] [--readonly] [--label <l>] [--json] -- <prompt>
        Start a subagent and print its id; it returns immediately.
  list [--json]
        List your subagents.
  status <id> [--json]
        Show the state (queued, running, done, failed, killed) of a subagent and the tail of its last output.
  send <id> [--json] -- <text>
        Message a subagent: "injected" into its running turn (claude/agy), "queued" behind its current turn, or "started" as a new turn of an idle one.
  wait [<id>...] [--any|--all] [--timeout <dur>] [--json]
        Block until the subagents finish (all of them by default, or the first with --any; no ids means all your running subagents) or the timeout (default and maximum 300s) passes. Prints each one's state and output tail, or still_running. Finished subagents are also reported to you without waiting.
  reply [--json] -- <text>
        Subagents only: message the agent that spawned you; it is injected into its running turn or runs as its next turn.
  kill <id> [--json]
        Stop a subagent and all subagents below it.
`

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// cli is what a command runs against.
type cli struct {
	client *sandboxsock.Client
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
}

// command runs one subcommand with the arguments after its name.
type command func(ctx context.Context, c *cli, args []string) int

// commands is the subcommand table; add new subcommands here.
var commands = map[string]command{
	"spawn":  cmdSpawn,
	"list":   cmdList,
	"status": cmdStatus,
	"send":   cmdSend,
	"wait":   cmdWait,
	"reply":  cmdReply,
	"kill":   cmdKill,
}

// run executes subagent (args includes the program name) and returns the exit
// code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[1] == "-h" || args[1] == "--help" || args[1] == "help" {
		_, _ = fmt.Fprint(stderr, usageText)
		if len(args) < 2 {
			return exitUsage
		}
		return exitOK
	}
	cmd, ok := commands[args[1]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "subagent: unknown command %q\n\n%s", args[1], usageText)
		return exitUsage
	}
	client, err := sandboxsock.New(0)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "subagent: %v\n", err)
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return cmd(ctx, &cli{client: client, stdout: stdout, stderr: stderr, now: time.Now}, args[2:])
}

// parsed is the command line of a subcommand.
type parsed struct {
	// positional are the arguments that are not flags.
	positional []string
	// rest are the arguments after "--" (the prompt or message).
	rest    []string
	hasRest bool
}

// parseArgs parses args with fs, allowing flags before and after the
// positional arguments. Everything after a lone "--" is returned verbatim in
// rest.
func parseArgs(fs *flag.FlagSet, args []string) (parsed, error) {
	var p parsed
	for i, a := range args {
		if a == "--" {
			p.rest, p.hasRest = args[i+1:], true
			args = args[:i]
			break
		}
	}
	fs.SetOutput(io.Discard)
	for {
		if err := fs.Parse(args); err != nil {
			return p, err
		}
		if fs.NArg() == 0 {
			return p, nil
		}
		p.positional = append(p.positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// usageError reports a bad command line.
func (c *cli) usageError(format string, a ...any) int {
	_, _ = fmt.Fprintf(c.stderr, "subagent: "+format+"\n\n%s", append(a, usageText)...)
	return exitUsage
}

// call posts in to path (or GETs it when in is nil) and decodes a 200 answer
// into out. A failure is printed and reported as false.
func (c *cli) call(ctx context.Context, path string, in, out any) bool {
	var resp *http.Response
	var err error
	if in == nil {
		resp, err = c.client.Get(ctx, path)
	} else {
		resp, err = c.client.PostJSON(ctx, path, in)
	}
	if err != nil {
		_, _ = fmt.Fprintf(c.stderr, "subagent: cannot reach the runner: %v\n", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(c.stderr, "subagent: %s\n", failure(resp))
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		_, _ = fmt.Fprintf(c.stderr, "subagent: unreadable answer from the runner: %v\n", err)
		return false
	}
	return true
}

// failure explains a non-200 answer.
func failure(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var e sandboxsock.ErrorResponse
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		return "not allowed: " + msg
	case http.StatusNotFound:
		return "not found: " + msg
	case http.StatusTooManyRequests:
		return "limit reached: " + msg
	case http.StatusServiceUnavailable:
		return "unavailable: " + msg
	}
	return msg
}

func (c *cli) printJSON(v any) int {
	enc := json.NewEncoder(c.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_, _ = fmt.Fprintf(c.stderr, "subagent: %v\n", err)
		return exitError
	}
	return exitOK
}

// parseFlags is parseArgs with the usage error handling of every command.
func (c *cli) parseFlags(fs *flag.FlagSet, args []string) (parsed, int, bool) {
	p, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(c.stderr, usageText)
			return p, exitOK, false
		}
		return p, c.usageError("%v", err), false
	}
	return p, exitOK, true
}

func cmdSpawn(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent to run (a teammate)")
	model := fs.String("model", "", "model override")
	readonly := fs.Bool("readonly", false, "mount the checkout read-only")
	label := fs.String("label", "", "short label")
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	// The prompt is what follows "--"; without it, the positional words.
	words := p.rest
	if !p.hasRest {
		words = p.positional
	} else if len(p.positional) > 0 {
		return c.usageError("unexpected argument %q before --", p.positional[0])
	}
	prompt := strings.Join(words, " ")
	if *agent == "" {
		return c.usageError("spawn needs --agent <name>")
	}
	if strings.TrimSpace(prompt) == "" {
		return c.usageError("spawn needs a prompt after --")
	}
	var out sandboxsock.SubagentSpawnResponse
	if !c.call(ctx, sandboxsock.PathSubagentSpawn, sandboxsock.SubagentSpawnRequest{
		AgentID: *agent, Model: *model, Readonly: *readonly, Label: *label, Prompt: prompt,
	}, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	_, _ = fmt.Fprintln(c.stdout, out.ID)
	return exitOK
}

func cmdList(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(p.positional) > 0 || p.hasRest {
		return c.usageError("list takes no arguments")
	}
	var out sandboxsock.SubagentListResponse
	if !c.call(ctx, sandboxsock.PathSubagentList, nil, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	if len(out.Subagents) == 0 {
		_, _ = fmt.Fprintln(c.stdout, "no subagents")
		return exitOK
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tLABEL\tAGENT\tMODEL\tMODE\tSTATE\tLAST ACTIVITY")
	for _, s := range out.Subagents {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, dash(s.Label), s.Agent, dash(s.Model), mode(s), s.State, c.ago(s.LastActivityMs))
	}
	_ = tw.Flush()
	for _, s := range out.Subagents {
		if s.State == stateQueued {
			_, _ = fmt.Fprintln(c.stdout, "\nqueued: waiting for the workspace; it starts once your current run and any other writer are done")
			break
		}
	}
	return exitOK
}

// idArg is the single id argument of status and kill.
func (c *cli) idArg(name string, p parsed) (string, int, bool) {
	if p.hasRest || len(p.positional) != 1 {
		return "", c.usageError("%s takes exactly one subagent id", name), false
	}
	return p.positional[0], exitOK, true
}

func cmdStatus(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	id, code, ok := c.idArg("status", p)
	if !ok {
		return code
	}
	var out sandboxsock.SubagentStatusResponse
	if !c.call(ctx, sandboxsock.PathSubagentStatus, sandboxsock.SubagentIDRequest{ID: id}, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	s := out.Subagent
	_, _ = fmt.Fprintf(c.stdout, "id: %s\nlabel: %s\nagent: %s\nmodel: %s\nmode: %s\nstate: %s\nlast activity: %s\n",
		s.ID, dash(s.Label), s.Agent, dash(s.Model), mode(s), s.State, c.ago(s.LastActivityMs))
	if s.State == stateQueued {
		_, _ = fmt.Fprintln(c.stdout, "\nqueued: waiting for the workspace; it starts once your current run and any other writer are done")
	}
	if s.Summary != "" {
		_, _ = fmt.Fprintf(c.stdout, "\nlast output (tail):\n%s\n", s.Summary)
	}
	return exitOK
}

func cmdSend(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	if !p.hasRest || len(p.positional) != 1 {
		return c.usageError("send needs one subagent id and the message after --")
	}
	text := strings.Join(p.rest, " ")
	if strings.TrimSpace(text) == "" {
		return c.usageError("send needs a message after --")
	}
	var out sandboxsock.SubagentSendResponse
	if !c.call(ctx, sandboxsock.PathSubagentSend, sandboxsock.SubagentSendRequest{ID: p.positional[0], Text: text}, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	_, _ = fmt.Fprintln(c.stdout, out.Delivery)
	return exitOK
}

// parseTimeout reads a --timeout value: a Go duration, or plain seconds.
func parseTimeout(v string) (time.Duration, error) {
	arg := v
	if _, err := strconv.Atoi(v); err == nil {
		v += "s"
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid --timeout %q (use e.g. 90s or 5m)", arg)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--timeout must be positive")
	}
	return d, nil
}

func cmdWait(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	anyOf := fs.Bool("any", false, "return when one subagent finished")
	all := fs.Bool("all", false, "wait for all subagents (default)")
	timeout := fs.String("timeout", "", "longest wait, e.g. 90s (default and maximum 300s)")
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	if p.hasRest {
		return c.usageError("wait takes subagent ids, not a message")
	}
	if *anyOf && *all {
		return c.usageError("wait takes --any or --all, not both")
	}
	req := sandboxsock.SubagentWaitRequest{IDs: p.positional, Any: *anyOf}
	if *timeout != "" {
		d, err := parseTimeout(*timeout)
		if err != nil {
			return c.usageError("%v", err)
		}
		req.TimeoutMs = d.Milliseconds()
	}
	var out sandboxsock.SubagentWaitResponse
	if !c.call(ctx, sandboxsock.PathSubagentWait, req, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	if len(out.Results) == 0 {
		_, _ = fmt.Fprintln(c.stdout, "no running subagents to wait for")
		return exitOK
	}
	for _, r := range out.Results {
		s := r.Subagent
		state := s.State
		if r.StillRunning {
			state = "still_running"
		}
		_, _ = fmt.Fprintf(c.stdout, "%s (%s): %s\n", s.ID, dash(s.Label), state)
		if s.Summary != "" {
			_, _ = fmt.Fprintf(c.stdout, "  last output (tail):\n%s\n", indent(s.Summary))
		}
		if r.Notes != "" {
			_, _ = fmt.Fprintf(c.stdout, "  note: %s\n", r.Notes)
		}
	}
	return exitOK
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

func cmdReply(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("reply", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	if !p.hasRest || len(p.positional) != 0 {
		return c.usageError("reply takes only the message after --")
	}
	text := strings.Join(p.rest, " ")
	if strings.TrimSpace(text) == "" {
		return c.usageError("reply needs a message after --")
	}
	var out sandboxsock.SubagentReplyResponse
	if !c.call(ctx, sandboxsock.PathSubagentReply, sandboxsock.SubagentReplyRequest{Text: text}, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	_, _ = fmt.Fprintln(c.stdout, out.Delivery)
	return exitOK
}

func cmdKill(ctx context.Context, c *cli, args []string) int {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, code, ok := c.parseFlags(fs, args)
	if !ok {
		return code
	}
	id, code, ok := c.idArg("kill", p)
	if !ok {
		return code
	}
	var out sandboxsock.SubagentKillResponse
	if !c.call(ctx, sandboxsock.PathSubagentKill, sandboxsock.SubagentIDRequest{ID: id}, &out) {
		return exitError
	}
	if *asJSON {
		return c.printJSON(out)
	}
	if out.Stopped == 0 {
		_, _ = fmt.Fprintf(c.stdout, "%s was not running\n", id)
		return exitOK
	}
	_, _ = fmt.Fprintf(c.stdout, "stopped %d run(s) of %s and its subagents\n", out.Stopped, id)
	return exitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func mode(s sandboxsock.SubagentInfo) string {
	if s.Readonly {
		return "readonly"
	}
	return "writable"
}

// ago renders a unix-ms time relative to now.
func (c *cli) ago(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := max(c.now().Sub(time.UnixMilli(ms)), 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
