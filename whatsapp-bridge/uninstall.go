package main

// Removal of everything the assistant installs outside the Claude extension
// itself: the background service, the installed binary, and (with --purge) the
// local message store. The Desktop Extension has to be removed from Claude's
// own Settings > Extensions — nothing here can do that.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func runUninstall(purge bool, assumeYes bool) {
	fmt.Println("WhatsApp Assistant — uninstall")
	fmt.Println()
	fmt.Println("This will:")
	fmt.Println("  • stop and remove the background sync service")
	fmt.Printf("  • delete %s\n", binDir())
	fmt.Printf("  • delete %s\n", logsDir())
	if purge {
		fmt.Printf("  • DELETE YOUR MESSAGE HISTORY at %s\n", storeDir())
		fmt.Println("    (including the WhatsApp link — you would need to scan the QR code again)")
	} else {
		fmt.Printf("  • KEEP your message history at %s\n", storeDir())
		fmt.Println("    (re-run setup later and everything comes back; use --purge to delete it)")
	}
	fmt.Println()

	if !assumeYes && !confirm("Continue? [y/N]: ") {
		fmt.Println("Cancelled — nothing was changed.")
		return
	}

	var done, failed []string

	// Stop and deregister the service first, so nothing is writing to the files
	// we are about to remove — and, on Windows, so the binary is not in use.
	if err := unregisterService(); err != nil {
		failed = append(failed, fmt.Sprintf("remove background service: %v", err))
	} else {
		done = append(done, "stopped and removed background service")
	}

	// Remove anything left by the pre-1.0 hand-built setup.
	removeLegacyService()

	for _, dir := range []string{binDir(), logsDir()} {
		if err := removeIfPresent(dir); err != nil {
			failed = append(failed, err.Error())
		} else {
			done = append(done, "removed "+dir)
		}
	}

	if purge {
		if err := removeIfPresent(storeDir()); err != nil {
			failed = append(failed, err.Error())
		} else {
			done = append(done, "removed message store")
		}
		// The app home is only ours to delete once everything inside is gone.
		os.Remove(setupLockPath())
		os.Remove(appHome())
	}

	fmt.Println()
	for _, d := range done {
		fmt.Println("  ✓ " + d)
	}
	for _, f := range failed {
		fmt.Println("  ✗ " + f)
	}

	fmt.Println()
	if len(failed) > 0 {
		fmt.Println("Some steps failed — see above.")
	}
	fmt.Println("One last step, which has to be done by hand:")
	fmt.Println("  Open Claude, go to Settings > Extensions, and remove \"WhatsApp Assistant\".")
	if !purge {
		fmt.Println()
		fmt.Printf("Your messages are still on this computer at:\n  %s\n", storeDir())
		fmt.Println("Delete that folder yourself, or re-run this uninstaller with --purge, to remove them.")
	}
	fmt.Println()
	fmt.Println("You may also want to open WhatsApp on your phone and remove this computer")
	fmt.Println("from Settings > Linked Devices.")
}

func removeIfPresent(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %v", path, err)
	}
	return nil
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
