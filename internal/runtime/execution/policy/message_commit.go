package policy

import "context"

type MessageCommitter interface {
	Reserve(kind string, targetID int64) (func(run func()), error)
	Shutdown(context.Context) error
}

type InlineCommitter struct{}

func (InlineCommitter) Reserve(string, int64) (func(func()), error) {
	return func(run func()) {
		if run != nil {
			run()
		}
	}, nil
}

func (InlineCommitter) Shutdown(context.Context) error { return nil }
