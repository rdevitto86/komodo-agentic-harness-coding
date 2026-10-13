VENV := $(HOME)/.venvs/komodo-docs

.PHONY: docsite dashboard

# Serve the HLD and LLD at http://127.0.0.1:8765/hld/, installing the docs tools on first run.
docsite: $(VENV)/bin/mkdocs
	PATH=$(VENV)/bin:$$PATH python3 docs/_site/build.py serve

# Create the docs environment outside the repo and install the pinned docs tools.
$(VENV)/bin/mkdocs: docs/_site/requirements.txt
	python3 -m venv $(VENV)
	$(VENV)/bin/pip install -q -r docs/_site/requirements.txt
	touch $@

# Serve the dashboard proof of concept on moving mock data; it prints its address with an access token.
dashboard:
	go run ./dashboard/poc
