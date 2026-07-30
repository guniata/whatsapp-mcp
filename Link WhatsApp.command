#!/bin/bash
# Double-click this file to link WhatsApp (one-time QR scan).
# A QR code appears below — scan it with your phone:
#   WhatsApp  ->  Settings  ->  Linked Devices  ->  Link a Device
# After it says it's connected and syncing, you can close this window.

cd "/Users/guniata/Repositories/whatsapp-mcp/whatsapp-bridge" || {
  echo "Could not find the WhatsApp bridge folder."; read -r; exit 1;
}

clear
echo "============================================================"
echo "  Linking WhatsApp — scan the QR code below with your phone"
echo "  WhatsApp > Settings > Linked Devices > Link a Device"
echo "============================================================"
echo

./whatsapp-bridge-app
