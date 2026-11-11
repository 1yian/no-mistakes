package agent

import (
	"bufio"
	"io"
	"strings"
)

type sseEvent struct {
	Name string // event: field (empty if not specified)
	Data string // concatenated data: fields
}

func parseSSE(r io.Reader, handler func(sseEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024*1024)
	var name string
	var dataLines []string

	flush := func() bool {
		if len(dataLines) == 0 && name == "" {
			return true
		}
		data := strings.Join(dataLines, "\n")
		cont := handler(sseEvent{Name: name, Data: data})
		name = ""
		dataLines = nil
		return cont
	}

	for scanner.Scan() {
		line := scanner.Text()
		// Trim trailing \r for \r\n line endings
		line = strings.TrimRight(line, "\r")

		if line == "" {
			// Empty line = event boundary
			if !flush() {
				return nil
			}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			name = strings.TrimPrefix(line[6:], " ")
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line[5:], " "))
		}
		// Ignore other fields (id:, retry:, comments)
	}

	// Flush any trailing event without final newline
	flush()

	return scanner.Err()
}
