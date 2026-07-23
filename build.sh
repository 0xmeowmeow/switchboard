#!/usr/bin/env bash
# build and install switchboard
set -e

if ! command -v go >/dev/null 2>&1; then
  echo "Go isn't installed. On Debian:"
  echo "  sudo apt install golang-go"
  echo "or, for a current version:"
  echo "  curl -sL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz"
  echo "  echo 'export PATH=\$PATH:/usr/local/go/bin' >> ~/.zshrc && source ~/.zshrc"
  exit 1
fi

echo "building..."
go build -o switchboard .

mkdir -p ~/.local/bin
cp switchboard ~/.local/bin/sb
echo "installed to ~/.local/bin/sb"

if ! echo "$PATH" | grep -q "$HOME/.local/bin"; then
  echo
  echo "add this to ~/.zshrc:"
  echo "  export PATH=\$HOME/.local/bin:\$PATH"
fi

echo
echo "run it with:  sb"
