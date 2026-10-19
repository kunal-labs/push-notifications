.PHONY: build test run clean help tidy-check

build:
	@./scripts/build.sh

test:
	@./scripts/test.sh

run:
	@./scripts/run.sh

clean:
	@./scripts/clean.sh

tidy-check:
	@./scripts/tidy-check.sh

help:
	@./scripts/help.sh
