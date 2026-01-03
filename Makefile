.PHONY: build install clean

BINARY=claude-code-slack-anywhere

build:
	go build -o $(BINARY)
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign -f -s - $(BINARY) 2>/dev/null || true; \
	fi

install: build
	mkdir -p ~/bin
	install -m 755 $(BINARY) ~/bin/$(BINARY)
	@echo "Installed to ~/bin/$(BINARY)"

clean:
	rm -f $(BINARY)
