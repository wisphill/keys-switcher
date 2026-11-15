package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const toolName = "keys-switcher"
const defaultAnthropicBaseURL = "http://127.0.0.1:3817/anthropic"

type Config struct {
	DefaultProfile   string             `yaml:"default_profile"`
	AnthropicBaseURL string             `yaml:"anthropic_base_url"`
	Profiles         map[string]Profile `yaml:"profiles"`
}

type Profile struct {
	SotatekAPIKey string `yaml:"sotatek_api_key"`
	Remote        string `yaml:"remote"`
}

// Backup lives below .git, so it is repository metadata and can never appear in git diff.
type BackupFile struct {
	Version int                     `json:"version"`
	Remotes map[string]RemoteBackup `json:"remotes"`
}

type RemoteBackup struct {
	URL     string `json:"url"`
	SavedAt string `json:"saved_at"`
	Profile string `json:"profile,omitempty"`
}

type ActiveEnvironment struct {
	AnthropicBaseURL   string `json:"anthropic_base_url"`
	AnthropicAuthToken string `json:"anthropic_auth_token"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "aikeys":
		err = remoteCommand(os.Args[2:])
	case "git":
		err = runGit(os.Args[2:])
	case "env":
		err = printEnvironment()
	case "shell-init":
		err = printShellInit(os.Args[2:])
	case "install":
		err = install()
	case "uninstall":
		err = uninstall()
	case "help", "--help", "-h":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q (use \"ks git <command>\")", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `ks wraps Git explicitly and uses saved remote URLs instead of switched URLs.

Usage:
  ks git <any git command and arguments>
  ks aikeys [--config FILE] [--remote NAME]
  ks aikeys switch [--config FILE] [--remote NAME] [PROFILE]
  ks aikeys restore [--remote NAME]
  ks aikeys status [--remote NAME]
  ks aikeys add [--config FILE] [--name PROFILE] [--origin URL] [--key API_KEY]
  ks aikeys list [--config FILE]
  ks env
  ks shell-init [zsh|bash|powershell]
  ks install | ks uninstall

Running "aikeys" interactively displays the profiles, marks the active one, and
prompts for a replacement. "aikeys switch" saves the current URL exactly once in .git/keys-switcher/remotes.json,
then changes the selected remote to the profile's rendered URL. A remote is
selected by --remote; otherwise "origin" is used if present, or the sole
remote is used. Every ks git invocation (for example, "ks git push" or
"ks git fetch") gets the saved URL injected only for that invocation; the
switched URL in .git/config is not used.
`)
}

func remoteCommand(args []string) error {
	if len(args) == 0 {
		return chooseProfile(nil)
	}
	switch args[0] {
	case "add":
		return addProfile(args[1:])
	case "list":
		return listProfiles(args[1:])
	case "switch":
		return switchRemote(args[1:])
	case "restore":
		return restoreRemote(args[1:])
	case "status":
		return showStatus(args[1:])
	default:
		if strings.HasPrefix(args[0], "-") {
			return chooseProfile(args)
		}
		return fmt.Errorf("unknown remote command %q", args[0])
	}
}

// chooseProfile is the interactive shortcut behind "ks aikeys".  Keep the
// normal switch command separate so it stays predictable for scripts.
func chooseProfile(args []string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("aikeys without a subcommand requires an interactive terminal; use \"aikeys switch PROFILE\"")
	}

	fs := flag.NewFlagSet("aikeys", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "YAML configuration file")
	remoteFlag := fs.String("remote", "", "Git remote name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("aikeys does not accept positional arguments; use \"aikeys switch PROFILE\"")
	}

	repo, err := findRepo()
	if err != nil {
		return err
	}
	config, err := loadConfig(configFile(*configPath, repo))
	if err != nil {
		return err
	}
	remote, err := selectRemote(repo, *remoteFlag)
	if err != nil {
		return err
	}
	currentURL, err := gitOutput(repo, "remote", "get-url", remote)
	if err != nil {
		return err
	}

	activeToken := ""
	if active, err := loadActiveEnvironment(); err == nil {
		activeToken = active.AnthropicAuthToken
	}
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	activeName := ""
	for _, name := range names {
		profile := config.Profiles[name]
		url, err := renderRemote(profile)
		if err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
		if url == currentURL || (activeToken != "" && profile.SotatekAPIKey == activeToken) {
			activeName = name
			break
		}
	}

	fmt.Printf("API-key profiles for remote %q:\n", remote)
	for i, name := range names {
		marks := make([]string, 0, 2)
		if name == activeName {
			marks = append(marks, "active")
		}
		if name == config.DefaultProfile {
			marks = append(marks, "default")
		}
		mark := ""
		if len(marks) > 0 {
			mark = " (" + strings.Join(marks, ", ") + ")"
		}
		fmt.Printf("  %d. %s%s\n", i+1, name, mark)
	}

	promptText := "Select a profile by number or name"
	if activeName != "" {
		promptText += fmt.Sprintf(" [Enter keeps %s]", activeName)
	}
	promptText += " (q to cancel): "
	fmt.Fprint(os.Stderr, promptText)
	answer, err := readPromptLine()
	if err != nil {
		return err
	}
	if answer == "q" || answer == "quit" {
		fmt.Println("Cancelled.")
		return nil
	}
	selected := answer
	if selected == "" {
		selected = activeName
	}
	if index, err := parseProfileIndex(selected, len(names)); err == nil {
		selected = names[index]
	}
	if _, ok := config.Profiles[selected]; !ok {
		return fmt.Errorf("profile %q is not defined", selected)
	}
	if selected == "" {
		return errors.New("select a profile")
	}

	switchArgs := make([]string, 0, 5)
	if *configPath != "" {
		switchArgs = append(switchArgs, "--config", *configPath)
	}
	if *remoteFlag != "" {
		switchArgs = append(switchArgs, "--remote", *remoteFlag)
	}
	switchArgs = append(switchArgs, selected)
	return switchRemote(switchArgs)
}

func parseProfileIndex(value string, count int) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > count {
		return 0, errors.New("not a profile number")
	}
	return number - 1, nil
}

func readPromptLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func listProfiles(args []string) error {
	fs := flag.NewFlagSet("aikeys list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", userConfigPath(), "YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("list does not accept positional arguments")
	}
	config, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Println("No API-key profiles.")
		return nil
	}
	var output strings.Builder
	for _, name := range names {
		profile := config.Profiles[name]
		defaultMark := ""
		if name == config.DefaultProfile {
			defaultMark = " (default)"
		}
		fmt.Fprintf(&output, "%s%s\n  origin: %s\n  key:    %s\n\n", name, defaultMark, profile.Remote, profile.SotatekAPIKey)
	}
	fmt.Print(output.String())
	return nil
}

func addProfile(args []string) error {
	fs := flag.NewFlagSet("switch-remote add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", userConfigPath(), "YAML configuration file")
	name := fs.String("name", "", "profile name (optional)")
	origin := fs.String("origin", "", "remote URL/template")
	key := fs.String("key", "", "Sotatek API key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("add does not accept positional arguments")
	}
	var err error
	if *origin == "" {
		*origin, err = prompt("Origin URL (use {{.APIKey}} where the key belongs): ", false)
		if err != nil {
			return err
		}
	}
	if *key == "" {
		*key, err = prompt("Sotatek API key: ", true)
		if err != nil {
			return err
		}
	}
	if *origin == "" || *key == "" {
		return errors.New("origin and API key are required")
	}
	if *name == "" {
		*name = profileName(*key)
	}

	config, err := loadConfigIfPresent(*configPath)
	if err != nil {
		return err
	}
	if config.Profiles == nil {
		config.Profiles = map[string]Profile{}
	}
	if _, exists := config.Profiles[*name]; exists {
		return fmt.Errorf("profile %q already exists; choose --name", *name)
	}
	config.Profiles[*name] = Profile{SotatekAPIKey: *key, Remote: *origin}
	if config.DefaultProfile == "" {
		config.DefaultProfile = *name
	}
	if err := saveConfig(*configPath, config); err != nil {
		return err
	}
	fmt.Printf("Added profile %q to %s\n", *name, *configPath)
	return nil
}

func prompt(label string, secret bool) (string, error) {
	fmt.Fprint(os.Stderr, label)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(value)), err
	}
	var value string
	_, err := fmt.Fscanln(os.Stdin, &value)
	return strings.TrimSpace(value), err
}

func profileName(key string) string {
	// Do not put the API key itself in the profile name or config output.
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("key-%x", sum[:4])
}

func switchRemote(args []string) error {
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "YAML configuration file")
	remoteFlag := fs.String("remote", "", "Git remote name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("switch accepts at most one profile name")
	}

	repo, err := findRepo()
	if err != nil {
		return err
	}
	config, err := loadConfig(configFile(*configPath, repo))
	if err != nil {
		return err
	}
	profileName := config.DefaultProfile
	if fs.NArg() == 1 {
		profileName = fs.Arg(0)
	}
	if profileName == "" {
		return errors.New("choose a profile or set default_profile")
	}
	profile, ok := config.Profiles[profileName]
	if !ok {
		return fmt.Errorf("profile %q is not defined", profileName)
	}
	url, err := renderRemote(profile)
	if err != nil {
		return fmt.Errorf("profile %q: %w", profileName, err)
	}
	remote, err := selectRemote(repo, *remoteFlag)
	if err != nil {
		return err
	}

	currentURL, err := gitOutput(repo, "remote", "get-url", remote)
	if err != nil {
		return err
	}
	backup, err := loadBackup(repo)
	if err != nil {
		return err
	}
	if backup.Remotes == nil {
		backup.Remotes = make(map[string]RemoteBackup)
	}
	if _, saved := backup.Remotes[remote]; !saved {
		backup.Remotes[remote] = RemoteBackup{URL: currentURL, SavedAt: time.Now().UTC().Format(time.RFC3339), Profile: profileName}
		if err := saveBackup(repo, backup); err != nil {
			return err
		}
	}
	if currentURL != url {
		if err := gitRun(repo, "remote", "set-url", remote, url); err != nil {
			return err
		}
	}
	if err := activateEnvironment(config, profile); err != nil {
		// Keep the operation all-or-nothing as much as Git permits.
		if currentURL != url {
			_ = gitRun(repo, "remote", "set-url", remote, currentURL)
		}
		return err
	}
	backupLocation, err := backupPath(repo)
	if err != nil {
		return err
	}
	fmt.Printf("Switched remote %q to profile %q. Original URL is stored in %s.\n", remote, profileName, backupLocation)
	fmt.Fprintln(os.Stderr, `Environment updated. For this already-open shell, run: eval "$(ks env)"`)
	return nil
}

func restoreRemote(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	remoteFlag := fs.String("remote", "", "Git remote name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := findRepo()
	if err != nil {
		return err
	}
	remote, err := selectRemote(repo, *remoteFlag)
	if err != nil {
		return err
	}
	backup, err := loadBackup(repo)
	if err != nil {
		return err
	}
	saved, ok := backup.Remotes[remote]
	if !ok {
		return fmt.Errorf("no backup exists for remote %q", remote)
	}
	if err := gitRun(repo, "remote", "set-url", remote, saved.URL); err != nil {
		return err
	}
	fmt.Printf("Restored remote %q to %s\n", remote, saved.URL)
	return nil
}

func showStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	remoteFlag := fs.String("remote", "", "Git remote name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := findRepo()
	if err != nil {
		return err
	}
	remote, err := selectRemote(repo, *remoteFlag)
	if err != nil {
		return err
	}
	current, err := gitOutput(repo, "remote", "get-url", remote)
	if err != nil {
		return err
	}
	backup, err := loadBackup(repo)
	if err != nil {
		return err
	}
	fmt.Printf("remote:  %s\ncurrent: %s\n", remote, current)
	if old, ok := backup.Remotes[remote]; ok {
		fmt.Printf("backup:  %s\nsaved:   %s\n", old.URL, old.SavedAt)
	} else {
		fmt.Println("backup:  (none)")
	}
	return nil
}

func runGit(args []string) error {
	if len(args) == 0 {
		return errors.New("expected a git command")
	}
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("expected a git command")
	}
	repo, err := findRepo()
	if err != nil {
		return err
	}
	backup, err := loadBackup(repo)
	if err != nil {
		return err
	}
	return gitRunWithBackup(repo, backup, args...)
}

func configFile(value, repo string) string {
	if value != "" {
		return value
	}
	local := filepath.Join(repo, ".keys-switcher.yaml")
	if _, err := os.Stat(local); err == nil {
		return local
	}
	return userConfigPath()
}

func userConfigPath() string {
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return filepath.Join(programData, toolName, "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".keys-switcher.yaml")
	}
	return filepath.Join(home, ".ks", "config.yaml")
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if len(config.Profiles) == 0 {
		return Config{}, errors.New("config has no profiles")
	}
	return config, nil
}

func loadConfigIfPresent(path string) (Config, error) {
	config, err := loadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Profiles: map[string]Profile{}}, nil
	}
	return config, err
}

func saveConfig(path string, config Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func activateEnvironment(config Config, profile Profile) error {
	baseURL := config.AnthropicBaseURL
	if baseURL == "" {
		baseURL = defaultAnthropicBaseURL
	}
	active := ActiveEnvironment{AnthropicBaseURL: baseURL, AnthropicAuthToken: profile.SotatekAPIKey}
	if err := saveActiveEnvironment(active); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := exec.Command("launchctl", "setenv", "ANTHROPIC_BASE_URL", active.AnthropicBaseURL).Run(); err != nil {
			return fmt.Errorf("set ANTHROPIC_BASE_URL: %w", err)
		}
		if err := exec.Command("launchctl", "setenv", "ANTHROPIC_AUTH_TOKEN", active.AnthropicAuthToken).Run(); err != nil {
			return fmt.Errorf("set ANTHROPIC_AUTH_TOKEN: %w", err)
		}
	}
	if runtime.GOOS == "windows" {
		if err := setWindowsUserEnvironment(active); err != nil {
			return err
		}
	}
	return nil
}

func setWindowsUserEnvironment(active ActiveEnvironment) error {
	// User scope needs no Administrator access, has no setx length limitation,
	// and is inherited by processes started after the environment change.
	script := "[Environment]::SetEnvironmentVariable('ANTHROPIC_BASE_URL'," + powershellQuote(active.AnthropicBaseURL) + ",'User'); " +
		"[Environment]::SetEnvironmentVariable('ANTHROPIC_AUTH_TOKEN'," + powershellQuote(active.AnthropicAuthToken) + ",'User'); " + windowsEnvironmentChangeNotification
	if err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run(); err != nil {
		return fmt.Errorf("persist Anthropic environment for the Windows user: %w", err)
	}
	return nil
}

func activeEnvironmentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".ks", "environment.json")
	}
	return filepath.Join(home, ".ks", "environment.json")
}

func shellEnvironmentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".ks", "env.sh")
	}
	return filepath.Join(home, ".ks", "env.sh")
}

func saveActiveEnvironment(active ActiveEnvironment) error {
	path := activeEnvironmentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(active, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return err
	}
	shell := "export ANTHROPIC_BASE_URL=" + shellQuote(active.AnthropicBaseURL) + "\n" +
		"export ANTHROPIC_AUTH_TOKEN=" + shellQuote(active.AnthropicAuthToken) + "\n"
	return os.WriteFile(shellEnvironmentPath(), []byte(shell), 0600)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func loadActiveEnvironment() (ActiveEnvironment, error) {
	data, err := os.ReadFile(activeEnvironmentPath())
	if err != nil {
		return ActiveEnvironment{}, fmt.Errorf("read active environment: %w", err)
	}
	var active ActiveEnvironment
	if err := json.Unmarshal(data, &active); err != nil {
		return ActiveEnvironment{}, fmt.Errorf("parse active environment: %w", err)
	}
	if active.AnthropicBaseURL == "" || active.AnthropicAuthToken == "" {
		return ActiveEnvironment{}, errors.New("active environment is incomplete")
	}
	return active, nil
}

func printEnvironment() error {
	active, err := loadActiveEnvironment()
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		fmt.Printf("$env:ANTHROPIC_BASE_URL = %s\n$env:ANTHROPIC_AUTH_TOKEN = %s\n", powershellQuote(active.AnthropicBaseURL), powershellQuote(active.AnthropicAuthToken))
		return nil
	}
	fmt.Printf("export ANTHROPIC_BASE_URL=%s\nexport ANTHROPIC_AUTH_TOKEN=%s\n", shellQuote(active.AnthropicBaseURL), shellQuote(active.AnthropicAuthToken))
	return nil
}

func powershellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func printShellInit(args []string) error {
	fs := flag.NewFlagSet("shell-init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binary := fs.String("binary", "ks", "path to the ks executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("shell-init accepts at most one shell name")
	}
	shell := "zsh"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	if fs.NArg() == 1 {
		shell = strings.ToLower(fs.Arg(0))
	}
	switch shell {
	case "zsh", "bash", "sh":
		command := shellQuote(*binary)
		fmt.Printf(`ks() {
  command %s "$@"
  local ks_status=$?
  if [ "$ks_status" -eq 0 ] && [ "$1" = "aikeys" ]; then
    case "$2" in
      ""|switch|--*) eval "$(command %s env)" ;;
    esac
  fi
  return "$ks_status"
}
`, command, command)
		return nil
	case "powershell", "pwsh":
		command := powershellQuote(*binary)
		fmt.Printf(`function ks {
  $ksBinary = Get-Command %s -CommandType Application | Select-Object -First 1 -ExpandProperty Source
  if (-not $ksBinary) { throw 'ks executable was not found on PATH.' }
  & $ksBinary @args
  $ksStatus = $LASTEXITCODE
  if ($ksStatus -eq 0 -and $args.Count -ge 1 -and $args[0] -eq 'aikeys' -and ($args.Count -eq 1 -or $args[1] -eq 'switch' -or $args[1].StartsWith('-'))) {
    & $ksBinary env | Invoke-Expression
  }
  $global:LASTEXITCODE = $ksStatus
}
`, command)
		return nil
	default:
		return fmt.Errorf("unsupported shell %q", shell)
	}
}

func renderRemote(profile Profile) (string, error) {
	if profile.Remote == "" {
		return "", errors.New("remote is required")
	}
	t, err := template.New("remote").Option("missingkey=error").Parse(profile.Remote)
	if err != nil {
		return "", fmt.Errorf("invalid remote template: %w", err)
	}
	var result bytes.Buffer
	if err := t.Execute(&result, struct{ APIKey string }{profile.SotatekAPIKey}); err != nil {
		return "", err
	}
	if result.Len() == 0 {
		return "", errors.New("rendered remote is empty")
	}
	return result.String(), nil
}

func findRepo() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("not inside a Git work tree")
	}
	return strings.TrimSpace(string(out)), nil
}

func selectRemote(repo, requested string) (string, error) {
	remotes, err := gitLines(repo, "remote")
	if err != nil {
		return "", err
	}
	if requested != "" {
		for _, r := range remotes {
			if r == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("remote %q does not exist", requested)
	}
	for _, r := range remotes {
		if r == "origin" {
			return r, nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	if len(remotes) == 0 {
		return "", errors.New("repository has no remotes")
	}
	return "", fmt.Errorf("repository has multiple remotes (%s); pass --remote", strings.Join(remotes, ", "))
}

func gitLines(repo string, args ...string) ([]string, error) {
	out, err := gitOutput(repo, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRun(repo string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func gitRunWithBackup(repo string, backup BackupFile, args ...string) error {
	gitArgs := []string{"-C", repo}
	for remote, saved := range backup.Remotes {
		currentURL, err := gitOutput(repo, "remote", "get-url", remote)
		if err != nil {
			return err
		}
		// Adding remote.<name>.url with -c creates an additional push URL rather
		// than replacing the configured one. URL rewriting affects only this Git
		// process, so the saved URL is used without changing .git/config or
		// adding a second destination for push.
		gitArgs = append(gitArgs, "-c", "url."+saved.URL+".insteadOf="+currentURL)
	}
	gitArgs = append(gitArgs, args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func install() error {
	if runtime.GOOS == "windows" {
		relaunched, err := ensureWindowsMachineScope("install")
		if err != nil || relaunched {
			return err
		}
	}
	target, err := installPath()
	if err != nil {
		return err
	}
	if err := copyCurrentExecutable(target); err != nil {
		return installPermissionError(err)
	}
	if runtime.GOOS == "windows" {
		if err := updateWindowsMachinePath(filepath.Dir(target), true); err != nil {
			return installPermissionError(err)
		}
	}
	fmt.Printf("Installed ks for all users at %s\n", target)
	return nil
}

func uninstall() error {
	if runtime.GOOS == "windows" {
		relaunched, err := ensureWindowsMachineScope("uninstall")
		if err != nil || relaunched {
			return err
		}
	}
	target, err := installPath()
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := updateWindowsMachinePath(filepath.Dir(target), false); err != nil {
			return installPermissionError(err)
		}
		// Windows cannot delete an executable while it is running. cmd performs the
		// deletion after this process has exited.
		if _, err := os.Stat(target); err == nil {
			return exec.Command("cmd", "/C", "ping 127.0.0.1 -n 2 >NUL & del /F /Q "+windowsQuote(target)).Start()
		}
	} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return installPermissionError(err)
	}
	fmt.Printf("Uninstalled ks from %s\n", target)
	return nil
}

func installPath() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramFiles")
		if base == "" {
			base = `C:\Program Files`
		}
		return filepath.Join(base, toolName, "ks.exe"), nil
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return "/usr/local/bin/ks", nil
	}
	return "", fmt.Errorf("system-wide install is unsupported on %s", runtime.GOOS)
}

func copyCurrentExecutable(target string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func updateWindowsMachinePath(directory string, add bool) error {
	quotedDir := strings.ReplaceAll(directory, "'", "''")
	if add {
		script := "$p=[Environment]::GetEnvironmentVariable('Path','Machine'); if (($p -split ';') -notcontains '" + quotedDir + "') {[Environment]::SetEnvironmentVariable('Path',($p.TrimEnd(';')+';" + quotedDir + "'),'Machine'); " + windowsEnvironmentChangeNotification + "}"
		return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run()
	}
	script := "$p=[Environment]::GetEnvironmentVariable('Path','Machine'); [Environment]::SetEnvironmentVariable('Path',(($p -split ';' | Where-Object {$_ -and $_ -ne '" + quotedDir + "'}) -join ';'),'Machine'); " + windowsEnvironmentChangeNotification
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

const windowsEnvironmentChangeNotification = `
$signature = @'
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError = true, CharSet = System.Runtime.InteropServices.CharSet.Auto)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
$native = Add-Type -MemberDefinition $signature -Name 'KeysSwitcherNative' -Namespace 'KeysSwitcher' -PassThru
$result = [UIntPtr]::Zero
[void]$native::SendMessageTimeout([IntPtr]0xffff, 0x001a, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
`

// ensureWindowsMachineScope relaunches this executable through UAC when it
// needs to write Program Files or the machine-wide PATH. The bool is true
// when the elevated child has already handled the requested operation.
func ensureWindowsMachineScope(command string) (bool, error) {
	const administratorCheck = "$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)"
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", administratorCheck).Output()
	if err != nil {
		return false, fmt.Errorf("check Windows Administrator permission: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(string(out)), "true") {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	fmt.Println("Requesting Administrator permission for a machine-wide operation...")
	// Start-Process with RunAs shows the standard UAC prompt. Waiting here
	// preserves the elevated child command's exit status for the caller.
	script := "$process = Start-Process -FilePath " + powershellQuote(executable) + " -ArgumentList " + powershellQuote(command) + " -Verb RunAs -Wait -PassThru; exit $process.ExitCode"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("Windows Administrator permission was declined or %s failed: %w", command, err)
	}
	return true, nil
}

func windowsQuote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"` }

func installPermissionError(err error) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("system-wide installation requires Administrator permission; accept the UAC prompt: %w", err)
	}
	return fmt.Errorf("system-wide installation requires root; run: sudo ks install (or sudo ks uninstall): %w", err)
}

func backupPath(repo string) (string, error) {
	// --git-path also works when .git is a file, as it is for linked worktrees.
	return gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-path", filepath.Join(toolName, "remotes.json"))
}

func loadBackup(repo string) (BackupFile, error) {
	path, err := backupPath(repo)
	if err != nil {
		return BackupFile{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return BackupFile{Version: 1, Remotes: map[string]RemoteBackup{}}, nil
	}
	if err != nil {
		return BackupFile{}, fmt.Errorf("read backup: %w", err)
	}
	var backup BackupFile
	if err := json.Unmarshal(data, &backup); err != nil {
		return BackupFile{}, fmt.Errorf("parse backup: %w", err)
	}
	if backup.Remotes == nil {
		backup.Remotes = map[string]RemoteBackup{}
	}
	return backup, nil
}

func saveBackup(repo string, backup BackupFile) error {
	path, err := backupPath(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
