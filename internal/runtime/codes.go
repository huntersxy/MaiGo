package runtime

// RestartExitCode Worker 请求 Runner 重启自身时使用的退出码，对齐 process_runner.RESTART_EXIT_CODE
const RestartExitCode = 42

// RunnerRelaunchExitCode 自更新成功后 Runner 交给新版本进程时的退出码，对齐 process_runner.RUNNER_RELAUNCH_EXIT_CODE
const RunnerRelaunchExitCode = 43
