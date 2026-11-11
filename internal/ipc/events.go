package ipc

type EventClass int

const (
	ClassActivity EventClass = iota
	ClassState
	ClassControl
)

func ClassOf(t EventType) EventClass {
	switch t {
	case EventLogChunk:
		return ClassActivity
	case EventStreamGap:
		return ClassControl
	default:
		return ClassState
	}
}
