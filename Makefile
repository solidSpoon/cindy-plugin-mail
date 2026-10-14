GO   ?= go
NODE ?= node

BIN        := mail-app-cli
GHOST_DIR  := ghost
GHOST_BIN  := $(GHOST_DIR)/bin/$(BIN)
SKILL_SRC  := skills/mail-app-cli
SKILL_DEST := $(HOME)/.agents/skills/mail-app-cli

.PHONY: build install test plugin plugin-universal verify bundle skill clean

build:
	$(GO) build

install: build
	$(GO) install

test:
	$(GO) test ./...

# Build once and put the same bytes in both consumers: ghost/bin/ (what the Cindy
# plugin bundles) and ~/.local/bin/ (what PATH and the agent skill call). Building
# them with two separate commands is how they drift apart silently.
plugin:
	$(GO) build -ldflags='-s -w' -o $(GHOST_BIN) .
	chmod 755 $(GHOST_BIN)
	install -m 755 $(GHOST_BIN) $(HOME)/.local/bin/$(BIN)
	@echo
	@echo "next: ghost_forge_install({ dir: \"$(CURDIR)/$(GHOST_DIR)\" })"

# The bundle should not be locked to one Mac architecture. Requires lipo (macOS).
plugin-universal:
	GOARCH=arm64 $(GO) build -ldflags='-s -w' -o $(GHOST_BIN).arm64 .
	GOARCH=amd64 $(GO) build -ldflags='-s -w' -o $(GHOST_BIN).amd64 .
	lipo -create -output $(GHOST_BIN) $(GHOST_BIN).arm64 $(GHOST_BIN).amd64
	rm -f $(GHOST_BIN).arm64 $(GHOST_BIN).amd64
	chmod 755 $(GHOST_BIN)
	@echo "architectures: $$(lipo -archs $(GHOST_BIN))"
	@if [ -d "$(HOME)/.local/bin" ]; then install -m 755 $(GHOST_BIN) "$(HOME)/.local/bin/$(BIN)"; echo "updated ~/.local/bin/$(BIN) with the same bytes"; fi

# Gates. These run identically here and in CI; see .github/workflows/.
verify:
	@test -x $(GHOST_BIN) || { echo "  FAIL  $(GHOST_BIN) is missing or not executable - run 'make plugin' or 'make plugin-universal' first"; exit 1; }
	$(NODE) tools/validate-ghost.mjs $(GHOST_DIR)
	@got="$$($(GO) version -m $(GHOST_BIN) | sed -n 's/.*vcs\.revision=//p' | sort -u)"; \
	want="$$(git rev-parse HEAD)"; \
	test "$$got" = "$$want" \
		|| { echo "  FAIL  $(GHOST_BIN) was built from '$$got', not HEAD ($$(git rev-parse --short HEAD)) - run 'make plugin-universal'"; exit 1; }
	@echo "  OK    $(GHOST_BIN) is built from HEAD"
	@$(GHOST_BIN) --version >/dev/null 2>&1 \
		|| { echo "  FAIL  $(GHOST_BIN) does not run on this machine (loader, code signature, arch or permission problem) - CI once shipped a darwin/arm64 binary that dyld refused to load"; exit 1; }
	@echo "  OK    $(GHOST_BIN) runs"
	@if [ -f "$(HOME)/.local/bin/$(BIN)" ]; then \
		if cmp -s $(GHOST_BIN) "$(HOME)/.local/bin/$(BIN)"; then \
			echo "  OK    ~/.local/bin/$(BIN) is byte-identical to the bundle"; \
		else \
			echo "  warn  ~/.local/bin/$(BIN) differs from the bundle - run 'make plugin' or 'make plugin-universal'"; \
		fi; \
	fi

# Reproduces what ghost_forge_pack emits: a zip of files only (no directory
# entries) rooted at the package root, Unix permissions preserved.
bundle: verify
	@V="$$($(NODE) -p "require('./$(GHOST_DIR)/ghost.json').version")"; \
	rm -f $(GHOST_DIR)/*.cindy; \
	out="$(BIN)-$$V.cindy"; \
	( cd $(GHOST_DIR) && zip -q -X -D -r "$$out" . -x '*.cindy' ); \
	echo "wrote $(GHOST_DIR)/$$out"

skill:
	rsync -a --delete $(SKILL_SRC)/ $(SKILL_DEST)/
	@echo "synced $(SKILL_SRC)/ -> $(SKILL_DEST)/"

clean:
	rm -f $(BIN) $(GHOST_BIN) $(GHOST_BIN).arm64 $(GHOST_BIN).amd64 $(GHOST_DIR)/*.cindy
