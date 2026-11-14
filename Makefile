APP := ks
ABS_APP := $(abspath $(APP))
ZSH_INIT := eval "$$($(ABS_APP) shell-init --binary $(ABS_APP) zsh)"

.PHONY: build test install uninstall enable-zsh-auto-env enable-powershell-auto-env print-zsh-init

build:
	go build -o $(APP) .

test: build
	go vet ./...
	GOOS=windows GOARCH=amd64 go build -o /tmp/ks.exe .

install: build
	sudo ./$(APP) install

uninstall:
	sudo $(APP) uninstall

print-zsh-init: build
	./$(APP) shell-init zsh

# Adds the wrapper once. Restart the terminal or run `source ~/.zshrc` after it.
enable-zsh-auto-env: build
	@if ! grep -Fqx '$(ZSH_INIT)' "$$HOME/.zshrc" 2>/dev/null; then \
		printf '\n# Auto-load Anthropic environment after ks aikeys switch\n%s\n' '$(ZSH_INIT)' >> "$$HOME/.zshrc"; \
	fi
	@echo 'Added ks shell integration. Run: source ~/.zshrc'

# Run this from GNU Make on Windows. It creates $PROFILE if needed and adds
# the PowerShell wrapper only once.
enable-powershell-auto-env: build
	powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/enable-powershell-auto-env.ps1 -KsPath "$(CURDIR)/ks.exe"
