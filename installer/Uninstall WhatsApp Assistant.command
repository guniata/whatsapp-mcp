#!/bin/bash
# Double-click this file to remove WhatsApp Assistant from your Mac.
# Your message history is kept unless you choose to delete it.

BIN="$HOME/Library/Application Support/WhatsAppAssistant/bin/whatsapp-assistant"

echo
if [ ! -x "$BIN" ]; then
  echo "WhatsApp Assistant does not appear to be installed on this Mac."
  echo "(Nothing found at: $BIN)"
  echo
  echo "If you installed the Claude extension, you can still remove it from"
  echo "Claude > Settings > Extensions."
  echo
  read -n 1 -s -r -p "Press any key to close this window."
  echo
  exit 0
fi

"$BIN" uninstall "$@"

echo
read -n 1 -s -r -p "Press any key to close this window."
echo
