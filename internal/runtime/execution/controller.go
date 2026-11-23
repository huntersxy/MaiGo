package execution

import (
	"context"
	"errors"
	"os"
	"strings"

	"MaiGo/internal/runtime/execution/committer"
	"MaiGo/internal/runtime/execution/policy"
	"MaiGo/internal/runtime/execution/scheduler"

	"go.uber.org/zap"
)

type MutateSettings func(*Profile, *FeatureToggles) error

type settings struct {
	configured  Profile
	effective   Profile
	toggles     FeatureToggles
	configError string
}

type Status struct {
	Configured       Profile
	Effective        Profile
	Features         FeatureToggles
	EnvMode          string
	ConfigError      string
	SessionScheduler any
}

type Controller struct {
	instanceDir string
	envMode     string
	policies    *policy.Registry
	settings    *policy.Slot[settings]
}

func NewController(ctx context.Context, instanceDir string) (*Controller, error) {
	controller := &Controller{
		instanceDir: instanceDir,
		envMode:     strings.TrimSpace(os.Getenv(EnvExecutionMode)),
		policies:    policy.NewRegistry(),
	}
	controller.settings, _ = policy.RegisterSlot[settings](controller.policies, "settings")
	configured, toggles, err := LoadRuntimeFile(instanceDir)
	configError := ""
	if err != nil {
		if errors.Is(err, ErrInvalidMode) {
			return nil, err
		}
		configError = err.Error()
		configured, toggles = DefaultProfile(), DefaultFeatureToggles()
	}
	next, err := controller.resolve(configured, toggles, configError)
	if err != nil {
		return nil, err
	}
	_, err = controller.policies.Update(ctx, func(*policy.Snapshot) ([]policy.Change, func() error, error) {
		return controller.changes(settings{}, next), nil, nil
	})
	if err != nil {
		return nil, err
	}
	return controller, nil
}

func (controller *Controller) Policies() *policy.Registry { return controller.policies }

func (controller *Controller) Status() Status {
	return controller.status(controller.policies.Snapshot())
}

func (controller *Controller) status(snapshot *policy.Snapshot) Status {
	active, _ := controller.settings.From(snapshot)
	scheduler, _ := controller.policies.SessionScheduler.From(snapshot)
	return Status{
		Configured:       active.configured,
		Effective:        active.effective,
		Features:         active.toggles,
		EnvMode:          controller.envMode,
		ConfigError:      active.configError,
		SessionScheduler: scheduler.Stats(),
	}
}

func (controller *Controller) Update(ctx context.Context, mutate MutateSettings) (Status, error) {
	return controller.apply(ctx, false, mutate)
}

func (controller *Controller) Reset(ctx context.Context) (Status, error) {
	return controller.apply(ctx, true, nil)
}

func (controller *Controller) apply(ctx context.Context, reset bool, mutate MutateSettings) (Status, error) {
	snapshot, err := controller.policies.Update(ctx, func(current *policy.Snapshot) ([]policy.Change, func() error, error) {
		active, _ := controller.settings.From(current)
		var edit MutateSettings
		if !reset {
			edit = func(profile *Profile, toggles *FeatureToggles) error {
				if *profile != active.configured || *toggles != active.toggles {
					return ErrConfigChanged
				}
				if mutate != nil {
					return mutate(profile, toggles)
				}
				return nil
			}
		}
		save, err := prepareRuntimeSave(controller.instanceDir, reset, edit)
		if err != nil {
			return nil, nil, err
		}
		next, err := controller.resolve(save.profile, save.toggles, "")
		if err != nil {
			return nil, nil, err
		}
		return controller.changes(active, next), save.persist, nil
	})
	if err != nil {
		return Status{}, err
	}
	return controller.status(snapshot), nil
}

func (controller *Controller) resolve(configured Profile, toggles FeatureToggles, configError string) (settings, error) {
	effective, err := applyEnvMode(configured, controller.envMode)
	return settings{configured: configured, effective: effective, toggles: toggles, configError: configError}, err
}

func (state settings) native(key policy.Key) bool {
	return NativeFeatureActive(state.effective, state.toggles, string(key))
}

func (controller *Controller) changes(current, next settings) []policy.Change {
	changes := []policy.Change{controller.settings.With(next)}
	if old, want := current.native(policy.SessionSchedulerKey), next.native(policy.SessionSchedulerKey); old != want ||
		(want && current.effective.WorkerPoolSize != next.effective.WorkerPoolSize) {
		var implementation policy.SessionScheduler = policy.DirectScheduler{}
		if want {
			implementation = nativeScheduler{Scheduler: scheduler.New(context.Background(), scheduler.Options{
				Workers: next.effective.WorkerPoolSize,
				OnError: func(sessionKey string, err error) {
					zap.L().Error("原生会话任务失败", zap.String("session_id", sessionKey), zap.Error(err))
				},
			})}
		}
		changes = append(changes, controller.policies.SessionScheduler.With(implementation))
	}
	if old, want := current.native(policy.MessageCommitterKey), next.native(policy.MessageCommitterKey); old != want {
		var implementation policy.MessageCommitter = policy.InlineCommitter{}
		if want {
			implementation = committer.NewNative()
		}
		changes = append(changes, controller.policies.MessageCommitter.With(implementation))
	}
	return changes
}

type nativeScheduler struct{ *scheduler.Scheduler }

func (adapter nativeScheduler) Submit(ctx context.Context, key string, task policy.SessionTask) error {
	return adapter.Scheduler.Submit(ctx, key, scheduler.Task(task))
}

func (adapter nativeScheduler) Stats() any { return adapter.Scheduler.Stats() }
