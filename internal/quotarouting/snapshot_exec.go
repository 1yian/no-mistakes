package quotarouting

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

const maxSnapshotBytes = 4 << 20

func ReadSnapshot(ctx context.Context, exe string, timeout time.Duration) (*Snapshot, error) {
	if exe == "" {
		exe = "quota-axi"
	}
	if timeout <= 0 {
		timeout = DefaultReadTimeout
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := snapshotCommand(readCtx, exe)
	shellenv.ConfigureShellCommand(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedBuffer{limit: maxSnapshotBytes, inner: &stdout}
	cmd.Stderr = &stderr
	if err := shellenv.RunShellCommand(cmd); err != nil {
		if readCtx.Err() != nil {
			return nil, fmt.Errorf("quota-axi did not finish within %s", timeout)
		}
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return nil, fmt.Errorf("quota-axi failed: %s", fallbackReason(reason))
	}
	snapshot, err := ParseSnapshot(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

type boundedBuffer struct {
	inner *bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.inner.Len() >= b.limit {
		return len(p), nil // discard beyond the bound; parse will refuse the truncated document
	}
	room := b.limit - b.inner.Len()
	if room < len(p) {
		b.inner.Write(p[:room])
		return len(p), nil
	}
	return b.inner.Write(p)
}

func fallbackReason(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const max = 160
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}

const DefaultReadTimeout = 15 * time.Second
