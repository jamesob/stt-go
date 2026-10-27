package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

var debugEnabled = os.Getenv("STT_DEBUG") != ""

func info(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func warn(format string, args ...any) {
	fmt.Printf("⚠️  "+format+"\n", args...)
}

func errorf(format string, args ...any) {
	fmt.Printf("❌ "+format+"\n", args...)
}

func debugf(format string, args ...any) {
	if debugEnabled {
		fmt.Printf("[debug] "+format+"\n", args...)
	}
}

func printReady(hotkeyID string) {
	fmt.Printf("\nReady │ Hold %s to record, +Shift ↵, Esc ✗\n", hotkeyName(hotkeyID))
}

// run runs a command with a timeout, discarding output.
func run(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

// runOut runs a command with a timeout and returns its stdout.
func runOut(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// runIn runs a command feeding input on stdin, with a timeout.
func runIn(timeout time.Duration, input []byte, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run()
}
