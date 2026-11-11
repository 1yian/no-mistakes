package intent

func AllReaders(disabled map[string]bool) []Reader {
	all := []Reader{
		NewClaudeReader(),
		NewCodexReader(),
		NewOpenCodeReader(),
		NewRovoDevReader(),
		NewPiReader(),
		NewCopilotReader(),
	}
	if len(disabled) == 0 {
		return all
	}
	out := make([]Reader, 0, len(all))
	for _, r := range all {
		if disabled[r.Name()] {
			continue
		}
		out = append(out, r)
	}
	return out
}
