package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"flexconnect/client/local"
	"flexconnect/internal/ipc"
	"flexconnect/internal/logging"
	"flexconnect/internal/types"
	"golang.org/x/term"
)

var verbose bool
var (
	cliIn  io.Reader = os.Stdin
	cliOut io.Writer = os.Stdout
	cliErr io.Writer = os.Stderr
)

type helpTopic struct {
	Name        string
	Summary     string
	Usage       string
	Description string
	Examples    []string
	Subcommands []helpTopic
}

func main() {
	parsedVerbose := false
	filteredArgs := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--verbose" {
			parsedVerbose = true
			continue
		}
		filteredArgs = append(filteredArgs, arg)
	}
	os.Args = append([]string{os.Args[0]}, filteredArgs...)

	socket := flag.String("socket", ipc.DefaultSocketPath(), "daemon socket or named pipe path")
	verboseShort := flag.Bool("v", false, "enable verbose debug output")
	verboseLong := flag.Bool("verbose", false, "same as -v")
	flag.Parse()
	verbose = parsedVerbose || *verboseShort || *verboseLong
	local.SetDebug(verbose)
	logging.Init(cliErr, condLevel(verbose), true)
	args := flag.Args()
	debugf("socket=%q verbose=%t args=%v", *socket, verbose, args)
	if len(args) == 0 || isHelpArg(args[0]) {
		_, _ = io.WriteString(cliOut, rootHelp())
		return
	}
	client := &local.Client{Socket: *socket}
	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := run(runCtx, client, args); err != nil {
		fmt.Fprintln(cliErr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, client *local.Client, args []string) error {
	if len(args) == 0 {
		_, err := io.WriteString(cliOut, rootHelp())
		return err
	}
	debugf("run command=%q args=%v", args[0], args)
	if args[0] == "help" {
		return printHelpTopic(args[1:])
	}
	switch args[0] {
	case "status":
		debugf("handling status")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("status")
		}
		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		debugf("status result state=%q current=%q", status.State, status.CurrentProfileID)
		if hasJSONFlag(args[1:]) {
			return printJSON(status)
		}
		profiles, _ := client.Profiles(ctx)
		_, err = io.WriteString(cliOut, formatStatusWithProfiles(status, profiles))
		return err
	case "login":
		debugf("handling login args=%v", args)
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("login")
		}
		return runLogin(ctx, client, args[1:])
	case "up":
		debugf("handling up")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("up")
		}
		return runUp(ctx, client, args[1:])
	case "down":
		debugf("handling disconnect")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("down")
		}
		if err := client.Disconnect(ctx); err != nil {
			return err
		}
		debugf("disconnect success")
		return printCurrentStatus(ctx, client)
	case "logs":
		debugf("handling logs")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("logs")
		}
		logs, err := client.Logs(ctx)
		if err != nil {
			return err
		}
		debugf("received %d logs", len(logs))
		return printJSON(logs)
	case "diag":
		debugf("handling diag")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("diag")
		}
		diag, err := client.Diagnostics(ctx)
		if err != nil {
			return err
		}
		if len(args) > 1 {
			path := args[1]
			data, err := json.MarshalIndent(diag, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
			debugf("diagnostics wrote to %q bytes=%d", path, len(data))
			_, err = fmt.Fprintf(cliOut, "Wrote diagnostics to %s\n", path)
			return err
		}
		debugf("diagnostics status=%q current=%q connected=%q profiles=%d logs=%d routes=%d",
			diag.Status.State, diag.Status.CurrentProfileID, diag.Status.ConnectedProfileID,
			len(diag.Profiles), len(diag.Logs), len(diag.Status.EffectiveRoutes))
		return printJSON(diag)
	case "traffic":
		debugf("handling traffic")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("traffic")
		}
		traffic, err := client.Traffic(ctx)
		if err != nil {
			return err
		}
		if hasJSONFlag(args[1:]) {
			return printJSON(traffic)
		}
		_, err = io.WriteString(cliOut, formatTrafficSnapshot(*traffic))
		return err
	case "profile":
		debugf("handling profile args=%v", args)
		return runProfile(ctx, client, args[1:])
	case "route":
		debugf("handling route args=%v", args)
		return runRoutes(ctx, client, args[1:])
	case "proxy":
		debugf("handling proxy args=%v", args)
		return runProxy(ctx, client, args[1:])
	case "watch":
		debugf("handling watch")
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("watch")
		}
		watcher, err := client.Watch(context.Background())
		if err != nil {
			return err
		}
		defer watcher.Close()
		for {
			notify, err := watcher.Next()
			if err != nil {
				return err
			}
			debugf("watch notify event=%q payload=%+v", notify.Event, notify)
			if err := printJSON(notify); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func runLogin(ctx context.Context, client *local.Client, args []string) error {
	if len(args) == 0 {
		req, err := promptLoginRequest(cliIn, cliOut)
		if err != nil {
			return err
		}
		if err := client.Login(ctx, req); err != nil {
			return err
		}
		return printCurrentStatus(ctx, client)
	}
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "server URL")
	user := fs.String("user", "", "username")
	password := fs.String("password", "", "password")
	name := fs.String("name", "", "profile name")
	group := fs.String("group", "", "VPN group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: flexconnect login [--server <url> --user <username> --password <password> --name <profile-name> --group <group>]")
	}
	if *server == "" || *user == "" {
		return fmt.Errorf("login requires --server and --user")
	}
	if err := client.Login(ctx, types.LoginRequest{
		Name:      *name,
		ServerURL: *server,
		Username:  *user,
		Group:     *group,
		Password:  *password,
	}); err != nil {
		return err
	}
	return printCurrentStatus(ctx, client)
}

func promptLoginRequest(in io.Reader, out io.Writer) (types.LoginRequest, error) {
	reader := bufio.NewReader(in)
	server, err := promptRequiredValue(reader, out, "Server URL")
	if err != nil {
		return types.LoginRequest{}, err
	}
	user, err := promptRequiredValue(reader, out, "Username")
	if err != nil {
		return types.LoginRequest{}, err
	}
	password, err := promptSecretValue(reader, in, out, "Password")
	if err != nil {
		return types.LoginRequest{}, err
	}
	name, err := promptValue(reader, out, "Profile name", true)
	if err != nil {
		return types.LoginRequest{}, err
	}
	group, err := promptValue(reader, out, "VPN group", true)
	if err != nil {
		return types.LoginRequest{}, err
	}
	return types.LoginRequest{
		Name:      name,
		ServerURL: server,
		Username:  user,
		Group:     group,
		Password:  password,
	}, nil
}

func promptRequiredValue(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	for {
		value, err := promptValue(reader, out, label, false)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		if _, err := fmt.Fprintf(out, "%s is required.\n", label); err != nil {
			return "", err
		}
	}
}

func promptValue(reader *bufio.Reader, out io.Writer, label string, optional bool) (string, error) {
	suffix := ""
	if optional {
		suffix = " (optional)"
	}
	if _, err := fmt.Fprintf(out, "%s%s: ", label, suffix); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value := strings.TrimSpace(line)
	if err == io.EOF && value == "" {
		return "", io.ErrUnexpectedEOF
	}
	return value, nil
}

func promptSecretValue(reader *bufio.Reader, in io.Reader, out io.Writer, label string) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return promptValue(reader, out, label, false)
	}
	if _, err := fmt.Fprintf(out, "%s: ", label); err != nil {
		return "", err
	}
	line, err := term.ReadPassword(int(file.Fd()))
	if _, writeErr := fmt.Fprintln(out); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(line)), nil
}

func runUp(ctx context.Context, client *local.Client, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profileName := fs.String("p", "", "profile name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: flexconnect up [-p <profile-name>]")
	}
	if *profileName != "" {
		id, err := findProfileIDByName(ctx, client, *profileName)
		if err != nil {
			return err
		}
		if err := client.Connect(ctx, id); err != nil {
			return err
		}
		return printCurrentStatus(ctx, client)
	}
	if _, err := client.CurrentProfile(ctx); err != nil {
		return fmt.Errorf("no profile selected; run `flexconnect login` first")
	}
	if err := client.ConnectCurrent(ctx); err != nil {
		return err
	}
	return printCurrentStatus(ctx, client)
}

func findProfileIDByName(ctx context.Context, client *local.Client, name string) (string, error) {
	profiles, err := client.Profiles(ctx)
	if err != nil {
		return "", err
	}
	match := ""
	for _, profile := range profiles {
		if profile.Name != name {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("multiple profiles named %q; rename one or use `flexconnect profile switch <id>`", name)
		}
		match = profile.ID
	}
	if match == "" {
		return "", fmt.Errorf("profile not found by name: %s", name)
	}
	return match, nil
}

func runProfile(ctx context.Context, client *local.Client, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		return printNamedHelp("profile")
	}
	debugf("runProfile args=%v", args)
	switch args[0] {
	case "list":
		debugf("profile list")
		profiles, err := client.Profiles(ctx)
		if err != nil {
			return err
		}
		debugf("profile list count=%d", len(profiles))
		return printJSON(profiles)
	case "add":
		debugf("profile add args=%v", args)
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("profile add")
		}
		if len(args) < 3 {
			return fmt.Errorf("usage: profile add <name> <server_url> [username] [password]")
		}
		profile := types.NewProfile(args[1])
		profile.ServerURL = args[2]
		if len(args) > 3 {
			profile.Username = args[3]
		}
		password := ""
		if len(args) > 4 {
			password = args[4]
		}
		debugf("profile add name=%q server=%q username=%q", profile.Name, profile.ServerURL, profile.Username)
		created, err := client.CreateProfile(ctx, profile, password)
		if err != nil {
			return err
		}
		debugf("profile add created id=%q", created.ID)
		return printJSON(created)
	case "current":
		debugf("profile current")
		profile, err := client.CurrentProfile(ctx)
		if err != nil {
			return err
		}
		debugf("profile current id=%q name=%q", profile.ID, profile.Name)
		return printJSON(profile)
	case "update":
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("profile update")
		}
		if len(args) < 1 {
			return fmt.Errorf("usage: profile update -p <profile-name> [--name ..] [--server ..] [--user ..] [--group ..] [--password ..] [--dns a,b] [--mtu 1399] [--accept true|false] [--auto-reconnect true|false] [--apply-dns true|false] [--include a,b] [--exclude c,d] [--socks5 true|false] [--socks5-listen 127.0.0.1:1080]")
		}
		fs := flag.NewFlagSet("profile update", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		profileName := fs.String("p", "", "profile name to update")
		name := fs.String("name", "", "new profile name")
		serverURL := fs.String("server", "", "new server URL")
		user := fs.String("user", "", "new username")
		group := fs.String("group", "", "new VPN group")
		password := fs.String("password", "", "new password")
		dns := fs.String("dns", "", "comma-separated DNS overrides")
		mtu := fs.String("mtu", "", "MTU override")
		accept := fs.String("accept", "", "accept server routes (true|false)")
		autoReconnect := fs.String("auto-reconnect", "", "reconnect automatically after unexpected disconnect (true|false)")
		applyDNS := fs.String("apply-dns", "", "apply DNS overrides to system DNS configuration (true|false)")
		fs.StringVar(autoReconnect, "auto_reconnect", "", "reconnect automatically after unexpected disconnect (true|false)")
		fs.StringVar(applyDNS, "apply_dns", "", "apply DNS overrides to system DNS configuration (true|false)")
		include := fs.String("include", "", "comma-separated custom include routes")
		exclude := fs.String("exclude", "", "comma-separated custom exclude routes")
		socks5 := fs.String("socks5", "", "enable SOCKS5 proxy (true|false)")
		socks5Listen := fs.String("socks5-listen", "", "SOCKS5 listen address")
		fs.StringVar(socks5Listen, "socks5_listen", "", "SOCKS5 listen address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return fmt.Errorf("usage: profile update -p <profile-name> [--name ..] [--server ..] [--user ..] [--group ..] [--password ..] [--dns a,b] [--mtu 1399] [--accept true|false] [--auto-reconnect true|false] [--apply-dns true|false] [--include a,b] [--exclude c,d] [--socks5 true|false] [--socks5-listen 127.0.0.1:1080]")
		}
		targetID := ""
		if *profileName != "" {
			id, err := findProfileIDByName(ctx, client, *profileName)
			if err != nil {
				return err
			}
			targetID = id
		} else {
			current, err := client.CurrentProfile(ctx)
			if err != nil {
				return fmt.Errorf("no profile selected; run `flexconnect login` first or provide -p <profile-name>")
			}
			targetID = current.ID
		}
		req := types.ProfileUpdateRequest{}
		if *name != "" {
			req.Name = name
		}
		if *serverURL != "" {
			req.ServerURL = serverURL
		}
		if *user != "" {
			req.Username = user
		}
		if *group != "" {
			req.Group = group
		}
		if *password != "" {
			req.Password = password
		}
		if *dns != "" {
			req.DNSOverrides = splitCSV(*dns)
		}
		if *mtu != "" {
			parsedMTU, err := strconv.Atoi(*mtu)
			if err != nil {
				return fmt.Errorf("invalid --mtu value: %w", err)
			}
			req.MTU = &parsedMTU
		}
		if *accept != "" {
			v, err := strconv.ParseBool(*accept)
			if err != nil {
				return fmt.Errorf("invalid --accept value: %w", err)
			}
			req.AcceptServerRoutes = &v
		}
		if *autoReconnect != "" {
			v, err := strconv.ParseBool(*autoReconnect)
			if err != nil {
				return fmt.Errorf("invalid --auto-reconnect value: %w", err)
			}
			req.AutoReconnect = &v
		}
		if *applyDNS != "" {
			v, err := strconv.ParseBool(*applyDNS)
			if err != nil {
				return fmt.Errorf("invalid --apply-dns value: %w", err)
			}
			req.ApplyDNS = &v
		}
		if *include != "" {
			req.CustomInclude = splitCSV(*include)
		}
		if *exclude != "" {
			req.CustomExclude = splitCSV(*exclude)
		}
		if *socks5 != "" {
			v, err := strconv.ParseBool(*socks5)
			if err != nil {
				return fmt.Errorf("invalid --socks5 value: %w", err)
			}
			req.SOCKS5Enabled = &v
		}
		if *socks5Listen != "" {
			req.SOCKS5Listen = socks5Listen
		}
		profile, err := client.UpdateProfile(ctx, targetID, req)
		if err != nil {
			return err
		}
		return printJSON(profile)
	case "switch":
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("profile switch")
		}
		if err := client.SwitchProfile(ctx, mustArg(args, 1, "profile id")); err != nil {
			return err
		}
		debugf("profile switch success id=%q", args[1])
		return nil
	case "remove":
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("profile remove")
		}
		if err := client.DeleteProfile(ctx, mustArg(args, 1, "profile id")); err != nil {
			return err
		}
		debugf("profile remove success id=%q", args[1])
		return nil
	default:
		return fmt.Errorf("unknown profile command: %s", args[0])
	}
}

func runRoutes(ctx context.Context, client *local.Client, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		return printNamedHelp("route")
	}
	debugf("runRoute args=%v", args)
	switch args[0] {
	case "show":
		debugf("route show")
		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		debugf("route show effective=%d", len(status.EffectiveRoutes))
		return printJSON(status.EffectiveRoutes)
	case "set":
		debugf("route set args=%v", args)
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("route set")
		}
		fs := flag.NewFlagSet("route set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		profileName := fs.String("p", "", "profile name to update")
		acceptRaw := ""
		acceptSet := false
		includeRaw := ""
		includeSet := false
		excludeRaw := ""
		excludeSet := false
		fs.Var(&stringFlag{value: &acceptRaw, set: &acceptSet}, "accept", "accept server routes (true|false)")
		fs.Var(&stringFlag{value: &includeRaw, set: &includeSet}, "include", "comma-separated include routes")
		fs.Var(&stringFlag{value: &excludeRaw, set: &excludeSet}, "exclude", "comma-separated exclude routes")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return fmt.Errorf("usage: route set [-p <profile-name>] [--accept true|false] [--include a,b] [--exclude c,d]")
		}
		if !acceptSet && !includeSet && !excludeSet {
			return fmt.Errorf("usage: route set [-p <profile-name>] [--accept true|false] [--include a,b] [--exclude c,d]")
		}
		targetID := *profileName
		if targetID == "" {
			current, err := client.CurrentProfile(ctx)
			if err != nil {
				return err
			}
			targetID = current.ID
		} else {
			id, err := findProfileIDByName(ctx, client, *profileName)
			if err != nil {
				return err
			}
			targetID = id
		}
		req := types.RouteUpdateRequest{}
		if acceptSet {
			v, err := strconv.ParseBool(acceptRaw)
			if err != nil {
				return fmt.Errorf("invalid --accept value: %w", err)
			}
			req.AcceptServerRoutes = &v
		}
		if includeSet {
			req.CustomInclude = splitCSV(includeRaw)
		}
		if excludeSet {
			req.CustomExclude = splitCSV(excludeRaw)
		}
		if includeSet && req.CustomInclude == nil && includeRaw == "" {
			req.CustomInclude = []string{}
		}
		if excludeSet && req.CustomExclude == nil && excludeRaw == "" {
			req.CustomExclude = []string{}
		}
		profile, err := client.UpdateRoutes(ctx, targetID, req)
		if err != nil {
			return err
		}
		debugf("route set updated profile=%q", targetID)
		return printJSON(profile)
	default:
		return fmt.Errorf("unknown route command: %s", args[0])
	}
}

func runProxy(ctx context.Context, client *local.Client, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		return printNamedHelp("proxy")
	}
	debugf("runProxy args=%v", args)
	switch args[0] {
	case "status":
		debugf("proxy status")
		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		if status.SOCKS5Enabled {
			_, err = fmt.Fprintf(cliOut, "SOCKS5: enabled on %s\n", status.SOCKS5Listen)
			return err
		}
		_, err = fmt.Fprintln(cliOut, "SOCKS5: disabled")
		return err
	case "enable":
		debugf("proxy enable args=%v", args)
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("proxy enable")
		}
		current, err := client.CurrentProfile(ctx)
		if err != nil {
			return err
		}
		req := types.ProfileUpdateRequest{}
		enabled := true
		req.SOCKS5Enabled = &enabled
		if len(args) > 1 {
			listen := args[1]
			req.SOCKS5Listen = &listen
		}
		profile, err := client.UpdateProfile(ctx, current.ID, req)
		if err != nil {
			return err
		}
		debugf("proxy enable profile=%q", current.ID)
		return printJSON(profile)
	case "disable":
		debugf("proxy disable args=%v", args)
		if wantCommandHelp(args[1:]) {
			return printNamedHelp("proxy disable")
		}
		current, err := client.CurrentProfile(ctx)
		if err != nil {
			return err
		}
		enabled := false
		profile, err := client.UpdateProfile(ctx, current.ID, types.ProfileUpdateRequest{SOCKS5Enabled: &enabled})
		if err != nil {
			return err
		}
		debugf("proxy disabled profile=%q", current.ID)
		return printJSON(profile)
	default:
		return fmt.Errorf("unknown proxy command: %s", args[0])
	}
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

type stringFlag struct {
	value *string
	set   *bool
}

func (f *stringFlag) Set(v string) error {
	*f.value = v
	if f.set != nil {
		*f.set = true
	}
	return nil
}

func (f *stringFlag) String() string {
	if f.value == nil {
		return ""
	}
	return *f.value
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "-json" {
			return true
		}
	}
	return false
}

func mustArg(args []string, index int, label string) string {
	if len(args) <= index {
		fmt.Fprintf(cliErr, "missing %s\n", label)
		os.Exit(2)
	}
	return args[index]
}

func printJSON(v any) error {
	enc := json.NewEncoder(cliOut)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printCurrentStatus(ctx context.Context, client *local.Client) error {
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	profiles, _ := client.Profiles(ctx)
	_, err = io.WriteString(cliOut, formatStatusWithProfiles(status, profiles))
	return err
}

func formatStatus(status *types.Status) string {
	return formatStatusWithProfiles(status, nil)
}

func formatStatusWithProfiles(status *types.Status, profiles []types.Profile) string {
	if status == nil {
		return "No status available.\n"
	}
	profileName := func(id string) string {
		for _, profile := range profiles {
			if profile.ID == id {
				if profile.Name != "" {
					return profile.Name
				}
				return profile.ServerURL
			}
		}
		return id
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "State: %s\n", status.State)
	if status.CurrentProfileID != "" {
		fmt.Fprintf(&buf, "Current Profile: %s\n", profileName(status.CurrentProfileID))
	}
	if status.ConnectedProfileID != "" {
		fmt.Fprintf(&buf, "Connected Profile: %s\n", profileName(status.ConnectedProfileID))
	}
	if status.Session != nil {
		fmt.Fprintf(&buf, "Server: %s\n", status.Session.ServerAddress)
		fmt.Fprintf(&buf, "VPN IP: %s\n", status.Session.VPNAddress)
		fmt.Fprintf(&buf, "Tunnel: %s (%s/%s)\n", status.Session.TUNName, status.Session.VPNAddress, status.Session.VPNMask)
		if len(status.Session.DNS) > 0 {
			fmt.Fprintf(&buf, "DNS: %s\n", strings.Join(status.Session.DNS, ", "))
		}
		if len(status.EffectiveRoutes) > 0 {
			fmt.Fprintf(&buf, "Routes: %d effective entries\n", len(status.EffectiveRoutes))
		}
	}
	if status.SOCKS5Enabled {
		fmt.Fprintf(&buf, "SOCKS5: %s\n", status.SOCKS5Listen)
	}
	if status.LastError != "" {
		fmt.Fprintf(&buf, "Last Error: %s\n", status.LastError)
	}
	if status.State == types.StateDisconnected && status.CurrentProfileID == "" {
		buf.WriteString("Next Step: run `flexconnect login` to add a connection.\n")
	} else if status.State == types.StateError {
		buf.WriteString("Next Step: run `flexconnect up` to retry or `flexconnect diag diag.json` for diagnostics.\n")
	}
	if status.UpdatedAt != "" {
		fmt.Fprintf(&buf, "Updated: %s\n", status.UpdatedAt)
	}
	return buf.String()
}

func formatTrafficSnapshot(traffic types.TrafficSnapshot) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Connected: %t\n", traffic.Connected)
	fmt.Fprintf(&buf, "Traffic Sent: %d B\n", traffic.BytesSent)
	fmt.Fprintf(&buf, "Traffic Received: %d B\n", traffic.BytesReceived)
	fmt.Fprintf(&buf, "Speed Sent: %.0f B/s\n", traffic.BytesSentPerSecond)
	fmt.Fprintf(&buf, "Speed Received: %.0f B/s\n", traffic.BytesReceivedPerSecond)
	if traffic.SampledAt != "" {
		fmt.Fprintf(&buf, "Sampled: %s\n", traffic.SampledAt)
	}
	return buf.String()
}

func usage() {
	_, _ = io.WriteString(cliOut, rootHelp())
}

func isHelpArg(arg string) bool {
	return arg == "help" || arg == "--help" || arg == "-h"
}

func wantCommandHelp(args []string) bool {
	return len(args) > 0 && isHelpArg(args[0])
}

func printHelpTopic(path []string) error {
	if len(path) == 0 {
		_, err := io.WriteString(cliOut, rootHelp())
		return err
	}
	return printNamedHelp(strings.Join(path, " "))
}

func printNamedHelp(name string) error {
	topic, ok := lookupHelpTopic(name)
	if !ok {
		return fmt.Errorf("unknown help topic: %s", name)
	}
	_, err := io.WriteString(cliOut, renderHelpTopic(topic))
	return err
}

func rootHelp() string {
	return renderHelpTopic(rootHelpTopic())
}

func debugf(format string, args ...any) {
	if !verbose {
		return
	}
	logging.WithComponent("flexconnect").Debugf(format, args...)
}

func condLevel(verbose bool) logging.Level {
	if verbose {
		return logging.LevelDebug
	}
	return logging.LevelInfo
}

func renderHelpTopic(topic helpTopic) string {
	var buf bytes.Buffer
	if topic.Usage != "" {
		fmt.Fprintf(&buf, "USAGE\n  %s\n", topic.Usage)
	}
	if topic.Description != "" {
		fmt.Fprintf(&buf, "\n%s\n", topic.Description)
	}
	if len(topic.Subcommands) > 0 {
		buf.WriteString("\nSUBCOMMANDS\n")
		width := 0
		for _, sub := range topic.Subcommands {
			if len(sub.Name) > width {
				width = len(sub.Name)
			}
		}
		for _, sub := range topic.Subcommands {
			fmt.Fprintf(&buf, "  %-*s  %s\n", width, sub.Name, sub.Summary)
		}
	}
	if len(topic.Examples) > 0 {
		buf.WriteString("\nEXAMPLES\n")
		for _, ex := range topic.Examples {
			fmt.Fprintf(&buf, "  %s\n", ex)
		}
	}
	buf.WriteString("\nGLOBAL FLAGS\n")
	buf.WriteString("  --socket <path>  path to daemon socket or named pipe\n")
	buf.WriteString("  -v, --verbose  enable verbose debug output\n")
	if topic.Name == "flexconnect" {
		buf.WriteString("\nFor command-specific help, run `flexconnect help <command>` or add `--help` after a command.\n")
	}
	return buf.String()
}

func rootHelpTopic() helpTopic {
	return helpTopic{
		Name:    "flexconnect",
		Summary: "CLI for the FlexConnect daemon",
		Usage:   "flexconnect [--socket <path>] [-v|--verbose] <command> [command flags]",
		Description: "FlexConnect controls the local FlexConnect daemon, manages VPN profiles,\n" +
			"starts AnyConnect sessions, and exposes local tools like diagnostics and SOCKS5 proxying.",
		Subcommands: []helpTopic{
			{Name: "status", Summary: "Show current daemon and VPN status"},
			{Name: "login", Summary: "Create a profile and log in"},
			{Name: "up", Summary: "Connect the current or named profile"},
			{Name: "down", Summary: "Disconnect the current VPN session"},
			{Name: "profile", Summary: "List, edit, and switch profiles"},
			{Name: "route", Summary: "Show or update per-profile route rules"},
			{Name: "proxy", Summary: "Control the built-in local SOCKS5 proxy"},
			{Name: "logs", Summary: "Show recent daemon logs"},
			{Name: "diag", Summary: "Export diagnostics as JSON"},
			{Name: "traffic", Summary: "Show traffic totals and speeds"},
			{Name: "watch", Summary: "Stream daemon events as NDJSON"},
		},
		Examples: []string{
			"flexconnect status",
			"flexconnect login",
			"flexconnect login --server https://vpn.example.com --user alice --password secret --name corp",
			"flexconnect up",
			"flexconnect up -p corp",
			"flexconnect down",
			"flexconnect traffic",
			"flexconnect profile update -p corp --user alice --server vpn.example.com --password secret --auto-reconnect true --apply-dns true --socks5-listen 127.0.0.1:1080",
			"flexconnect proxy enable 127.0.0.1:1080",
		},
	}
}

func lookupHelpTopic(name string) (helpTopic, bool) {
	topics := map[string]helpTopic{
		"status": {
			Name:        "status",
			Usage:       "flexconnect status [--json]",
			Description: "Show the current daemon state, active profile, session details, routes, and local SOCKS5 proxy status.",
			Examples:    []string{"flexconnect status", "flexconnect status --json"},
		},
		"login": {
			Name:        "login",
			Usage:       "flexconnect login [--server <url> --user <username> --password <password> --name <profile-name> --group <group>]",
			Description: "Create or update a profile, log in, and keep it as the last used profile. With no flags, prompts for the connection details interactively.",
			Examples: []string{
				"flexconnect login",
				"flexconnect login --server https://vpn.example.com --user alice --password secret --name corp",
			},
		},
		"up": {
			Name:        "up",
			Usage:       "flexconnect up [-p <profile-name>]",
			Description: "Connect the last used profile, or the named profile when -p is provided.",
			Examples:    []string{"flexconnect up", "flexconnect up -p corp"},
		},
		"down": {
			Name:        "down",
			Usage:       "flexconnect down",
			Description: "Disconnect the active VPN session and stop any per-profile local proxy.",
			Examples:    []string{"flexconnect down"},
		},
		"logs": {
			Name:        "logs",
			Usage:       "flexconnect logs",
			Description: "Print recent daemon logs as JSON.",
		},
		"diag": {
			Name:        "diag",
			Usage:       "flexconnect diag [file]",
			Description: "Print diagnostics as JSON or write them to a file.",
			Examples:    []string{"flexconnect diag", "flexconnect diag diag.json"},
		},
		"traffic": {
			Name:        "traffic",
			Usage:       "flexconnect traffic [--json]",
			Description: "Show VPN traffic totals and sampled upload/download speeds.",
			Examples:    []string{"flexconnect traffic", "flexconnect traffic --json"},
		},
		"watch": {
			Name:        "watch",
			Usage:       "flexconnect watch",
			Description: "Stream daemon notifications as newline-delimited JSON.",
		},
		"profile": {
			Name:        "profile",
			Usage:       "flexconnect profile <subcommand>",
			Description: "Manage stored VPN profiles.",
			Subcommands: []helpTopic{
				{Name: "list", Summary: "List all profiles"},
				{Name: "current", Summary: "Show the current profile"},
				{Name: "add", Summary: "Create a profile"},
				{Name: "update", Summary: "Update profile fields"},
				{Name: "switch", Summary: "Switch current profile"},
				{Name: "remove", Summary: "Delete a profile"},
			},
			Examples: []string{
				"flexconnect profile list",
				"flexconnect profile add corp https://vpn.example.com alice secret",
				"flexconnect profile update -p corp --socks5 true --socks5-listen 127.0.0.1:1080",
			},
		},
		"profile add": {
			Name:        "profile add",
			Usage:       "flexconnect profile add <name> <server_url> [username] [password]",
			Description: "Create a new profile and optionally seed username and password.",
		},
		"profile switch": {
			Name:        "profile switch",
			Usage:       "flexconnect profile switch <profile-id>",
			Description: "Switch the daemon's current profile. If another profile is connected, it reconnects using the new profile.",
		},
		"profile remove": {
			Name:        "profile remove",
			Usage:       "flexconnect profile remove <profile-id>",
			Description: "Delete a profile and its stored secret reference.",
		},
		"profile update": {
			Name:  "profile update",
			Usage: "flexconnect profile update -p <profile-name> [--name ..] [--server ..] [--user ..] [--group ..] [--password ..] [--dns a,b] [--mtu 1399] [--accept true|false] [--auto-reconnect true|false] [--apply-dns true|false] [--include a,b] [--exclude c,d] [--socks5 true|false] [--socks5-listen 127.0.0.1:1080]",
			Description: "Update profile fields in place. Runtime-relevant changes reconnect an active profile automatically.\n" +
				"Use `socks5=true` to enable the built-in SOCKS5 proxy for that profile.",
		},
		"route": {
			Name:        "route",
			Usage:       "flexconnect route <subcommand>",
			Description: "Inspect or change per-profile route behavior.",
			Subcommands: []helpTopic{
				{Name: "show", Summary: "Show effective routes from current status"},
				{Name: "set", Summary: "Update include/exclude route rules"},
			},
			Examples: []string{
				"flexconnect route show",
				"flexconnect route set --accept true --include 10.0.0.0/8 --exclude 1.1.1.1/32",
				"flexconnect route set -p corp --accept false --include 10.0.0.0/8 --exclude 1.1.1.1/32",
			},
		},
		"route set": {
			Name:        "route set",
			Usage:       "flexconnect route set [-p <profile-name>] [--accept true|false] [--include a,b] [--exclude c,d]",
			Description: "Update route preferences for a profile. If that profile is connected, FlexConnect reapplies the connection.",
		},
		"proxy": {
			Name:        "proxy",
			Usage:       "flexconnect proxy <subcommand>",
			Description: "Control the built-in local SOCKS5 proxy that can follow a profile.",
			Subcommands: []helpTopic{
				{Name: "status", Summary: "Show current SOCKS5 status"},
				{Name: "enable", Summary: "Enable SOCKS5 for current profile"},
				{Name: "disable", Summary: "Disable SOCKS5 for current profile"},
			},
			Examples: []string{
				"flexconnect proxy status",
				"flexconnect proxy enable 127.0.0.1:1080",
				"flexconnect proxy disable",
			},
		},
		"proxy enable": {
			Name:        "proxy enable",
			Usage:       "flexconnect proxy enable [listen-address]",
			Description: "Enable the built-in SOCKS5 proxy on the current profile. The listener starts automatically when that profile connects.",
		},
		"proxy disable": {
			Name:        "proxy disable",
			Usage:       "flexconnect proxy disable",
			Description: "Disable the built-in SOCKS5 proxy on the current profile.",
		},
	}
	topic, ok := topics[name]
	return topic, ok
}
