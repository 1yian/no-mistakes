package types

type ResponseOrigin string

const (
	ResponseOriginUnknown     ResponseOrigin = "unknown"
	ResponseOriginInteractive ResponseOrigin = "interactive"
	ResponseOriginAutoYes     ResponseOrigin = "auto_yes"
)

func (o ResponseOrigin) OrUnknown() ResponseOrigin {
	switch o {
	case ResponseOriginInteractive, ResponseOriginAutoYes:
		return o
	default:
		return ResponseOriginUnknown
	}
}
