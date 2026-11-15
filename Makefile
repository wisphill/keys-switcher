ifeq ($(OS),Windows_NT)
# GNU Make inherits SHELL from its parent process.  Git's sh.exe path contains
# spaces, which can make recipe startup fail from PowerShell.  Use cmd.exe for
# Windows recipes and invoke Git Bash explicitly where it is required.
SHELL := cmd.exe
APP := ks.exe
GIT_BASH ?= C:/Program Files/Git/bin/bash.exe
BASH_PROFILE ?= $(USERPROFILE)/.bashrc
SHELL_INSTALL_TARGETS := enable-all-shells
INSTALL_COMMAND := ./$(APP) install
UNINSTALL_COMMAND := ./$(APP) uninstall
else
APP := ks
GIT_BASH ?= bash
ifeq ($(shell uname -s),Darwin)
SHELL_INSTALL_TARGETS := enable-zsh-auto-env
else
SHELL_INSTALL_TARGETS := enable-bash-auto-env
endif
INSTALL_COMMAND := sudo ./$(APP) install
UNINSTALL_COMMAND := sudo ./$(APP) uninstall
endif

ABS_APP := $(abspath $(APP))
ZSH_INIT := eval "$$($(ABS_APP) shell-init --binary $(ABS_APP) zsh)"

.PHONY: build test install uninstall install-shell enable-zsh-auto-env enable-bash-auto-env enable-powershell-auto-env enable-all-shells print-zsh-init powershell

build:
	go build -o $(APP) .

test: build
	go vet ./...
	GOOS=windows GOARCH=amd64 go build -o /tmp/ks.exe .

install: build
	$(INSTALL_COMMAND)

uninstall:
	$(UNINSTALL_COMMAND)

install-shell: $(SHELL_INSTALL_TARGETS)

print-zsh-init: build
	./$(APP) shell-init zsh

# Adds the wrapper once. Restart the terminal or run `source ~/.zshrc` after it.
enable-zsh-auto-env: build
	@if ! grep -Fqx '$(ZSH_INIT)' "$$HOME/.zshrc" 2>/dev/null; then \
		printf '\n# Auto-load Anthropic environment after ks aikeys switch\n%s\n' '$(ZSH_INIT)' >> "$$HOME/.zshrc"; \
	fi
	@echo 'Added ks shell integration. Run: source ~/.zshrc'

enable-bash-auto-env: build
	"$(GIT_BASH)" scripts/enable-bash-auto-env.sh "$(ABS_APP)" "$(BASH_PROFILE)"

# Installs ks and configures PowerShell and Git Bash for every local Windows
# user profile. The script requests UAC once when required.
enable-all-shells: build
	powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/enable-all-shells.ps1 -KsPath "$(ABS_APP)" -GitBashPath "$(GIT_BASH)"

# Run this from GNU Make on Windows. It creates $PROFILE if needed and adds
# the PowerShell wrapper only once.
enable-powershell-auto-env: build
	powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/enable-powershell-auto-env.ps1 -KsPath "$(CURDIR)/ks.exe"

powershell: enable-powershell-auto-env
