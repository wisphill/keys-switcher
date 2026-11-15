# keys-switcher

A cross-platform Go CLI for switching a Git remote between Sotatek API-key profiles.

The original URL is saved at `.git/keys-switcher/remotes.json`, which is Git metadata and therefore never appears in a repository diff. The first `switch` for each remote writes the backup; further switches do not overwrite it.

## Install

```sh
go build -o ks .
# Move or copy ks into a directory on PATH.
```

To install the currently running binary for every user on a machine:

```sh
# macOS (requires an administrator account)
sudo ./ks install

# Windows: `ks install` shows a UAC prompt when needed.
.\ks.exe install
```

This installs `ks` at `/usr/local/bin/ks` on macOS and at
`C:\Program Files\keys-switcher\ks.exe` on Windows. The Windows command adds
that directory to the machine-wide `PATH` and notifies Windows of the
environment change; newly opened terminals see it immediately.
Remove it with the corresponding elevated command: `sudo ks uninstall` on
macOS, or `ks.exe uninstall` on Windows (it also prompts for UAC when needed).

## Configuration

By default, macOS/Linux uses `~/.ks/config.yaml`. Windows uses the machine-wide
`C:\ProgramData\keys-switcher\config.yaml`, so every Windows user runs against
the same key profiles. Create or modify that file from an Administrator
PowerShell, or pass `--config FILE` to use a different config. A repository
local `.keys-switcher.yaml` still takes precedence.

Copy `keys-switcher.example.yaml` to the chosen location. The profile remote is a Go template and can use `{{.APIKey}}`.

```yaml
default_profile: primary
profiles:
  primary:
    sotatek_api_key: "key-1"
    remote: "https://gateway.example/repo.git?api_key={{.APIKey}}"
  secondary:
    sotatek_api_key: "key-2"
    remote: "https://gateway.example/repo.git?api_key={{.APIKey}}"
```

## Usage

Run commands from the target Git repository:

```sh
ks aikeys                         # choose a profile interactively
ks aikeys switch --config ~/.ks/config.yaml secondary
ks aikeys status
ks aikeys restore
```

`ks aikeys` shows the available profiles and marks the profile currently in
use as `active`. Select another profile by typing its number or name. Press
Enter to keep the active profile, or type `q` to cancel. The shortcut requires
an interactive terminal; use `ks aikeys switch PROFILE` for scripts and CI.

### Add a key interactively

`add` asks for an origin URL and API key, creates a non-secret profile name,
and creates the default config directory and file. On Windows, run it from an
Administrator PowerShell to write the machine-wide config:

```sh
ks aikeys add
```

For CI or scripting, supply both values explicitly. Put `{{.APIKey}}` in the
origin wherever the provider expects the key:

```sh
ks aikeys add \
  --origin 'https://gateway.example/repo.git?api_key={{.APIKey}}' \
  --key "$SOTATEK_API_KEY" \
  --name quota-2
```

Use `--config FILE` to add to a specific YAML file. The generated config has
file permission `0600` on macOS/Linux. Avoid committing it.

List profiles:

```sh
ks aikeys list
```

The command prints every profile's origin and API key directly to the terminal.
Treat it like `cat` on a secret file: do not run it in recorded/shared terminals
or paste its output into logs.

## Anthropic environment

On every successful `ks aikeys switch`, the selected profile's key becomes the
active Anthropic token:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:3817/anthropic
export ANTHROPIC_AUTH_TOKEN='<selected virtual key>'
```

Set a different `anthropic_base_url` at the top level of the YAML if needed.
The active values are stored privately in `~/.ks/environment.json` and
`~/.ks/env.sh` (both mode `0600` on macOS/Linux).

macOS also updates the current user's `launchctl` environment. Windows uses
PowerShell/.NET to persist the values in the current user's environment (no
Administrator access or `setx` required); newly started Windows apps inherit
them. A process cannot alter the environment of the shell that launched it.
After switching, refresh the current Bash or zsh session with:

```sh
eval "$(ks env bash)"
```

On PowerShell, refresh the current terminal with:

```powershell
ks env | Invoke-Expression
```

Windows also persists these values in the current user's environment and
notifies Windows, so newly opened terminals inherit them. A running terminal
still needs the command above (or the PowerShell wrapper below), because a
child process cannot modify its parent shell's environment.

To make this automatic after every successful `ks aikeys switch`, install the
shell integration once:

```sh
make install-shell
```

On Windows, this requests UAC once, installs `ks` machine-wide, and configures
Windows PowerShell plus Git Bash for every local user profile. On macOS it
configures zsh, and on Linux it configures Bash. Open a new shell to load the
changes.

For manual setup on macOS with zsh:

```sh
echo 'eval "$(ks shell-init zsh)"' >> ~/.zshrc
source ~/.zshrc
```

When using the source checkout without system installation, prefer
`make enable-zsh-auto-env`: it writes the checkout's absolute `ks` path into
`~/.zshrc`, so `ks` does not need to be in `PATH`.

For interactive Git Bash over SSH on Windows, run
`make enable-bash-auto-env` on the SSH host as the account that will use `ks`.
It adds the integration to that account's `~/.bashrc` once, using the absolute
path to the built binary in the checkout. Run `source ~/.bashrc` to activate it
in the current shell. New shells that read `~/.bashrc` load the last selected
key at startup. For Bash login shells, source `~/.bashrc` from `~/.bash_profile`
if needed. Override `GIT_BASH` when Git is installed at a custom path.

On PowerShell, add this to your `$PROFILE` and restart the terminal:

```powershell
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $PROFILE) | Out-Null
ks shell-init powershell | Add-Content $PROFILE
```

If GNU Make is available on Windows, `make enable-powershell-auto-env` performs
the same setup idempotently (it will not append the wrapper twice).

The wrapper invokes the real `ks` binary first, then runs `eval "$(ks env bash)"`
after a successful `ks aikeys` interactive selection or `ks aikeys switch`.

Never commit or print `~/.ks/environment.json`.

`origin` is selected when it exists. For a repository without `origin`, the CLI selects its only remote. For more than one non-`origin` remote, select explicitly:

```sh
ks aikeys switch --config ~/.ks/config.yaml --remote upstream secondary
```

## Safe push wrapper

`ks git` is a transparent Git wrapper. After switching, every Git command receives the backed-up remote URL in temporary Git configuration, while `.git/config` remains unchanged. For example:

```sh
ks git status
ks git remote -v
ks git fetch
ks git pull
ks git push --force-with-lease
ks git switch main
```

This means a normal `git push` still uses the fake API-key endpoint currently stored in `.git/config`, but `ks git push` uses the origin saved by `ks aikeys switch`. It also applies to every saved remote, including non-`origin` names. Before the first switch there is no saved URL, `ks git` behaves exactly like Git.
