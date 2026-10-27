//go:build darwin

package main

func newPlatformBackend() (*Backend, error) {
	return &Backend{
		Recorder:         sharedRecorder,
		Listener:         &darwinListener{},
		Overlay:          darwinOverlay{},
		Inject:           injectText,
		PlaySound:        playSound,
		CheckPermissions: func() bool { return axTrusted(true) },
		PermissionHelp: "Grant these to your terminal app (not \"sttg\"):\n" +
			"  • Accessibility     — System Settings → Privacy & Security → Accessibility\n" +
			"  • Input Monitoring  — System Settings → Privacy & Security → Input Monitoring\n" +
			"Then restart the terminal and run sttg again.",
		ListenerHelp: "If this is a permissions problem, enable Input Monitoring for your\n" +
			"terminal app in System Settings → Privacy & Security, then restart it.",
	}, nil
}

func hotkeyName(id string) string {
	if name, ok := hotkeyDisplayNames[id]; ok {
		return name
	}
	return id
}
