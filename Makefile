VENV := $(HOME)/.venvs/komodo-docs
KOMODO_BIN ?= $(HOME)/.komodo/bin/komodo
GATE_OSES := linux darwin windows

.PHONY: docsite gate

# Serve the HLD and LLD at http://127.0.0.1:8765/hld/, installing the docs tools on first run.
docsite: $(VENV)/bin/mkdocs
	PATH=$(VENV)/bin:$$PATH python3 docsite/build.py serve

# Create the docs environment outside the repo and install the pinned docs tools.
$(VENV)/bin/mkdocs: docsite/requirements.txt
	python3 -m venv $(VENV)
	$(VENV)/bin/pip install -q -r docsite/requirements.txt
	touch $@

# Build, vet and test, build for each of the 3 operating systems, then place the development build at KOMODO_BIN.
gate:
	go build ./...
	go vet ./...
	go test ./...
	@for os in $(GATE_OSES); do \
		echo "GOOS=$$os go build ./... && GOOS=$$os go vet ./..."; \
		GOOS=$$os go build ./... && GOOS=$$os go vet ./... || exit 1; \
	done
	@mkdir -p "$(dir $(KOMODO_BIN))"
	@tmp=$$(mktemp "$(KOMODO_BIN).XXXXXX") && \
		{ go build -o "$$tmp" ./cmd/komodo && chmod 0755 "$$tmp" && mv -f "$$tmp" "$(KOMODO_BIN)"; } || \
		{ rm -f "$$tmp"; exit 1; }
	@echo "placed $(KOMODO_BIN)"
