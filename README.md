# sttg

**Push-to-talk speech-to-text for macOS and Linux, in a single Go binary.**

A minimal-dependency rewrite of [stt](https://github.com/jamesob/stt) (my fork
of [bokan/stt](https://github.com/bokan/stt)): hold a key,
speak, release — your words are typed wherever your cursor is. Transcription is
done by **your own self-hosted, OpenAI-compatible server**; no cloud APIs, no
Python, no model runtimes on the laptop.

## What changed vs. the Python version

- **Go, zero third-party modules.** The standard library plus, on macOS, a small
  amount of cgo against system frameworks (CoreGraphics event tap for the hotkey,
  AudioQueue for capture, Cocoa for the overlay). On Linux, hotkey capture reads
  `/dev/input` via evdev directly, audio capture streams raw PCM from
  `pw-record`/`arecord`, and typing/sounds shell out to `wtype`/`xdotool` and
  `paplay`; the overlay is cgo against `libwayland-client`/cairo/pango using the
  `wlr-layer-shell` protocol (sway, river, Hyprland, ...). Typing on macOS goes
  through `osascript`/`pbcopy`, sounds through `afplay`.
- **One provider.** Any server exposing `POST {base}/audio/transcriptions`
  (OpenAI audio API shape): [speaches](https://github.com/speaches-ai/speaches)
  (formerly faster-whisper-server), [LocalAI](https://localai.io),
  [vLLM](https://docs.vllm.ai) with a Whisper model, etc. Groq, MLX and
  Parakeet are gone.
- **New: replay.** The raw WAVs of the last 3 recordings are always kept on
  disk (`~/.config/stt/recent/`), surviving restarts — so if your server was
  down, nothing is lost. While holding the hotkey, tap **↑** to open the replay
  picker in the overlay; **↑/↓** cycles through the recent recordings, releasing
  the hotkey re-sends the selected one to the server, **Esc** cancels. The
  arrow keys are swallowed while picking on macOS, so they never reach the
  focused app (on Linux, evdev is listen-only, so they also reach the app).
- **Kept:** the recording overlay (mic icon + live waveform pill; Cocoa on
  macOS, wlr-layer-shell on Wayland), hold-to-record hotkey, Shift → trailing Enter, Esc → cancel, silence/too-short
  detection, `slash foo` → `/foo`, context prompt, retries, clipboard-restore
  after paste (macOS), the first-run setup wizard and `--config`, live config
  reload, recording archive with a 1GB cap, single-instance lock.
- **Stripped:** menu bar app, prompt overlay, auto-update check, issue capture.

## Requirements

### macOS

- macOS (Apple Silicon or Intel) with Xcode command line tools (`xcode-select --install`)
- A reachable OpenAI-compatible transcription server

### Linux

- Go 1.22+ to build, plus a C compiler and the cgo deps for the overlay:
  `pkg-config libwayland-client cairo pangocairo`
  (Debian/Ubuntu: `sudo apt install build-essential libwayland-dev libcairo2-dev libpango1.0-dev`)
- A reachable OpenAI-compatible transcription server
- `pw-record` (from `pipewire-utils`) or `arecord` (from `alsa-utils`) for audio
- `wtype` (Wayland) **or** `xdotool` (X11) for typing the result
- `paplay`/`aplay` for sound feedback (optional)
- Your user in the `input` group so evdev can read `/dev/input/event*`:

  ```bash
  sudo usermod -aG input $USER   # then log out and back in
  ```

  Debian/Ubuntu: `sudo apt install xdotool paplay alsa-utils`
  Arch: `sudo pacman -S xdotool wtype pipewire alsa-utils`

## Build & install

Requires Go 1.22+ (`go version`). Building on macOS needs Xcode command line
tools for the cgo frameworks; on Linux the overlay is cgo too — install the
deps above, or build with `CGO_ENABLED=0` to drop it (no overlay UI).

```bash
make build          # produces ./sttg
make install        # copies it to ~/.local/bin
```

## Server quickstart (example: speaches)

On any box with a GPU (or CPU for small models):

```bash
docker run --gpus=all -p 8000:8000 ghcr.io/speaches-ai/speaches:latest-cuda
```

Then point sttg at it:

```
STT_BASE_URL=http://your-server:8000/v1
STT_MODEL=Systran/faster-whisper-large-v3
```

## Permissions

**macOS** — grant these to **your terminal app** (iTerm2, Terminal, etc.) — not "sttg":

- **Accessibility** — System Settings → Privacy & Security → Accessibility
- **Input Monitoring** — System Settings → Privacy & Security → Input Monitoring
- **Microphone** — you'll be prompted on first recording

Restart the terminal after granting.

**Linux** — evdev reads `/dev/input/event*` directly, so no desktop permission
is needed; you just need read access to those devices (see the `input` group
note in Requirements). On Wayland, text injection needs `wtype`; on X11,
`xdotool`.

## Usage

```bash
sttg
```

On first run, a setup wizard walks you through server, model, audio device,
and hotkey (unless the Python version already initialized the config).
Run `sttg --config` to reconfigure at any time.

| Action | Keys |
|--------|------|
| Record | Hold **Right Command** (macOS) / **Right Super** (Linux, default) |
| Record + Enter | Hold **Shift** while recording |
| Replay a recent recording | While holding the hotkey, tap **↑** (then **↑/↓** to pick, release to send) |
| Cancel recording / picker / stuck transcription | **Esc** |
| Quit | **Ctrl+C** |

## Configuration

Config is **shared with the Python version**, including its `config.yml`
format. Precedence, exactly like Python: **OS environment** > **`./.env`** in
the working directory > **`~/.config/stt/config.yml`** (the Python version's
current format — general settings plus `backends`/`order`; when it exists the
global `.env` is ignored) > **`~/.config/stt/.env`** (legacy). The wizard
(`sttg --config`) writes `config.yml` — general keys at top level, server keys
under `backends.<name>` — exactly like Python's `save_config`; with no YAML
anywhere it falls back to editing `.env` in place. Changes are picked up live
(1s poll), including the hotkey, language, prompt, audio device, and server
settings — no restart needed.

An existing Python config works unmodified: `hotkey`, `audio_device`,
`sound_enabled`, `keep_recordings`, and the first backend's
`openai_base_url`/`openai_api_key`/`openai_whisper_model` are honored (an
empty or missing model means auto-detect from the server's `/models` list, as
in Python). `WHISPER_CPP_HTTP_URL` and `WHISPER_MODEL` are honored as
fallbacks for the server URL and model. `PROVIDER`/`groq`/`mlx`/`parakeet`
backends are ignored (there is only one provider now, and a cloud key is never
sent to your self-hosted server). The preferred server keys are:

```bash
# Base URL of your OpenAI-compatible server (include /v1 if the server uses it)
STT_BASE_URL=http://localhost:8000/v1

# API key, only if your server enforces one
STT_API_KEY=

# Model name the server expects; empty = auto-detect from GET /models
STT_MODEL=whisper-1

# Per-request transcription timeout in seconds
STT_TIMEOUT_S=30

# --- same keys as the Python version ---
LANGUAGE=en
PROMPT=Claude, Anthropic, TypeScript, Go
HOTKEY=cmd_r            # cmd_r, cmd_l, alt_r, alt_l, ctrl_r, ctrl_l, shift_r, shift_l
                        # (cmd_* = Right/Left Super on Linux)
AUDIO_DEVICE=           # System Settings name (macOS) or PipeWire source node
                        # (Linux); empty = system default
SOUND_ENABLED=true
KEEP_RECORDINGS=false   # archive everything to ~/.stt-recordings (1GB cap)
```

Independent of `KEEP_RECORDINGS`, the **last 3 recordings are always saved** to
`~/.config/stt/recent/` (WAV + transcript) for the replay feature.

The lock file is shared with the Python version too, so both can't run at
once and fight over the hotkey.

Set `STT_DEBUG=1` for verbose event logging.

## Development

```bash
make test     # unit tests (core logic is platform-independent and tested)
make vet
make fmt
```

The platform layer is isolated behind small interfaces (`Recorder`,
`HotkeyListener`, `Overlay`) in `backend.go`. The darwin implementation lives
in `*_darwin.go` (cgo), the Linux implementation in `*_linux.go` (pure Go);
`config.yml` is read/written by a small in-tree YAML subset in `yml.go`.
