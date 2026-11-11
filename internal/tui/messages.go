package tui

import (
	"github.com/kunchenguid/no-mistakes/internal/branchsync"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type eventMsg struct {
	event          ipc.Event
	subscriptionID uint64
}

type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

type subscriptionErrMsg struct {
	err            error
	subscriptionID uint64
}

type rerunStartedMsg struct {
	run       *ipc.RunInfo
	requestID uint64
}

type rerunErrMsg struct {
	err       error
	requestID uint64
}

type spinnerTickMsg struct{}

type syncRefreshedMsg struct{ state branchsync.State }
type syncAppliedMsg struct{ state branchsync.State }

type runReconciledMsg struct {
	run            *ipc.RunInfo
	err            error
	subscriptionID uint64
}

type stepDiffMsg struct {
	step           types.StepName
	diff           string
	truncated      bool
	err            error
	requestID      uint64
	subscriptionID uint64
}

type stepDiffRequest struct {
	step      types.StepName
	requestID uint64
}

type stepDiffReadyMsg struct {
	step           types.StepName
	requestID      uint64
	subscriptionID uint64
}

type resubscribeMsg struct{ subscriptionID uint64 }

type connectedMsg struct {
	events         <-chan ipc.Event
	cancelSub      func()
	subscriptionID uint64
}
