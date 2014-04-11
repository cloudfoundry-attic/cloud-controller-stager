package metrics

import (
	"github.com/cloudfoundry-incubator/metricz/instrumentation"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

type TaskInstrument struct {
	bbs bbs.MetricsBBS
}

func NewTaskInstrument(metricsBbs bbs.MetricsBBS) *TaskInstrument {
	return &TaskInstrument{bbs: metricsBbs}
}

func (t *TaskInstrument) Emit() instrumentation.Context {
	allRunOnces, err := t.bbs.GetAllRunOnces()
	if err != nil {
		panic("ARRGH!")
	}

	var (
		pendingCount   int
		claimedCount   int
		runningCount   int
		completedCount int
		resolvingCount int
	)

	for _, runOnce := range allRunOnces {
		switch runOnce.State {
		case models.RunOnceStatePending:
			pendingCount++
		case models.RunOnceStateClaimed:
			claimedCount++
		case models.RunOnceStateRunning:
			runningCount++
		case models.RunOnceStateCompleted:
			completedCount++
		case models.RunOnceStateResolving:
			resolvingCount++
		}
	}

	return instrumentation.Context{
		Name: "Tasks",
		Metrics: []instrumentation.Metric{
			{
				Name:  "Pending",
				Value: pendingCount,
			},
			{
				Name:  "Claimed",
				Value: claimedCount,
			},
			{
				Name:  "Running",
				Value: runningCount,
			},
			{
				Name:  "Completed",
				Value: completedCount,
			},
			{
				Name:  "Resolving",
				Value: resolvingCount,
			},
		},
	}
}
