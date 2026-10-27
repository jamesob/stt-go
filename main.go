package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.2.0"

var lockFile *os.File

// The lock file name matches the Python version so both can't run at once
// (they would fight over the hotkey and microphone).
func acquireLock() bool {
	path := filepath.Join(os.TempDir(), "stt.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d", os.Getpid())
	lockFile = f
	return true
}

func releaseLock() {
	if lockFile == nil {
		return
	}
	syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	lockFile.Close()
	os.Remove(lockFile.Name())
	lockFile = nil
}

// runConfigWizard runs the interactive setup, saving to the global config
// file (like the Python wizard's force_global saves).
func runConfigWizard(cfg *Config, reconfigure bool) {
	devices, defaultDevice := platformInputDevices()
	var lastPath string
	save := func(key, value string) {
		path, err := saveConfig(key, value, true)
		if err != nil {
			warn("Failed to save %s: %v", key, err)
			return
		}
		lastPath = path
	}
	runSetup(os.Stdin, os.Stdout, cfg, devices, defaultDevice, probeServer, save, reconfigure)
	if lastPath == "" {
		lastPath = filepath.Join(configDir(), "config.yml")
	}
	info("Saved to %s", lastPath)
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	configure := flag.Bool("config", false, "run the configuration wizard and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("sttg " + version)
		return
	}

	// Matches the Python version's headless guard.
	if os.Getenv("STT_HEADLESS") == "1" {
		fmt.Println("STT_HEADLESS=1 set; UI disabled")
		os.Exit(1)
	}

	cfg, cfgPath, err := loadConfig()
	if err != nil {
		errorf("Failed to load config: %v", err)
		os.Exit(1)
	}

	// --config: interactive wizard, like the Python version.
	if *configure {
		runConfigWizard(cfg, true)
		return
	}

	if !acquireLock() {
		errorf("Another instance of stt is already running")
		os.Exit(1)
	}

	// First-run onboarding (marker shared with the Python version).
	if isFirstRun() {
		runConfigWizard(cfg, false)
		markInitialized()
		if reloaded, _, err := loadConfig(); err == nil {
			cfg = reloaded
		}
	}

	fmt.Println()
	fmt.Println("sttg — push-to-talk speech-to-text (self-hosted, OpenAI-compatible)")
	fmt.Printf("Config: %s\n\n", cfgPath)

	client := newOpenAIClient(cfg)
	client.checkServer()

	backend, err := newPlatformBackend()
	if err != nil {
		errorf("%v", err)
		releaseLock()
		os.Exit(1)
	}

	if !backend.CheckPermissions() {
		fmt.Println()
		errorf("Missing required permissions.")
		if backend.PermissionHelp != "" {
			fmt.Println(backend.PermissionHelp)
		}
		releaseLock()
		os.Exit(1)
	}

	overlay := backend.Overlay
	if overlay == nil {
		overlay = nullOverlay{}
	}
	store := newRecentStore(cfg.RecentDir, recentMax)

	app := newApp(cfg, backend.Recorder, client, backend.Inject, backend.PlaySound)
	app.overlay = overlay
	app.recent = store

	ctrl := newController(app, backend.Listener, overlay, store, cfg.Hotkey)
	if err := ctrl.Start(); err != nil {
		errorf("Failed to start key listener: %v", err)
		if backend.ListenerHelp != "" {
			fmt.Println(backend.ListenerHelp)
		}
		releaseLock()
		os.Exit(1)
	}

	// Live config reload, like the Python version's ConfigWatcher.
	lastCfg := cfg
	watcher := startConfigWatcher(configPaths(), time.Second, func() {
		newCfg, _, err := loadConfig()
		if err != nil {
			return
		}
		changes := diffConfigs(lastCfg, newCfg)
		if len(changes) == 0 {
			return
		}
		info("Config reloaded: %s", strings.Join(changes, ", "))
		app.SetConfig(newCfg)
		serverChanged := false
		for _, key := range changes {
			switch key {
			case "STT_BASE_URL", "STT_API_KEY", "STT_MODEL", "STT_TIMEOUT_S":
				serverChanged = true
			case "HOTKEY":
				if err := ctrl.SetHotkey(newCfg.Hotkey); err != nil {
					warn("Hotkey change failed: %v", err)
				} else {
					info("   Hotkey: %s", hotkeyName(newCfg.Hotkey))
				}
			case "LANGUAGE":
				info("   Language: %s", newCfg.Language)
			case "PROMPT":
				info("   Prompt: %s", orDefaultStr(newCfg.Prompt, "(empty)"))
			case "AUDIO_DEVICE":
				info("   Audio device: %s", orDefaultStr(newCfg.AudioDevice, "default"))
			case "SOUND_ENABLED":
				info("   Sound: %s", enabledStr(newCfg.SoundEnabled))
			case "KEEP_RECORDINGS":
				info("   Keep recordings: %s", enabledStr(newCfg.KeepRecordings))
			}
		}
		if serverChanged {
			app.SetTranscriber(newOpenAIClient(newCfg))
			info("   Server: %s (model %s)", newCfg.BaseURL, newCfg.Model)
		}
		lastCfg = newCfg
	})

	printReady(cfg.Hotkey)
	fmt.Println("Ctrl+C to quit")

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("\nBye")
		watcher.Stop()
		ctrl.Stop()
		releaseLock()
		os.Exit(0)
	}()

	// On macOS this runs the Cocoa main loop (required for the overlay) and
	// never returns; elsewhere it just blocks.
	runMainLoop()
}

func enabledStr(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
