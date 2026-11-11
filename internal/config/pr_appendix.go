package config

import "fmt"

const (
	PRUpdateTextKeep       = "keep"
	PRUpdateTextRegenerate = "regenerate"

	PRAppendixFull      = "full"
	PRAppendixCollapsed = "collapsed"
	PRAppendixMinimal   = "minimal"
)

func (p PR) AppendixMode() string {
	switch p.Appendix {
	case PRAppendixCollapsed, PRAppendixMinimal, PRAppendixFull:
		return p.Appendix
	default:
		return PRAppendixFull
	}
}

func (p PR) KeepsUpdateText() bool {
	return p.UpdateText == PRUpdateTextKeep
}

func validatePRUpdateText(value string) error {
	switch value {
	case "", PRUpdateTextKeep, PRUpdateTextRegenerate:
		return nil
	default:
		return fmt.Errorf("pr.update_text: %q is not a valid mode (want %q or %q)", value, PRUpdateTextKeep, PRUpdateTextRegenerate)
	}
}

func validatePRAppendix(mode string) error {
	switch mode {
	case "", PRAppendixFull, PRAppendixCollapsed, PRAppendixMinimal:
		return nil
	default:
		return fmt.Errorf("pr.appendix: %q is not a valid mode (want %q, %q, or %q)", mode, PRAppendixFull, PRAppendixCollapsed, PRAppendixMinimal)
	}
}
