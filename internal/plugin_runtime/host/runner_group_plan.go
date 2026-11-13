package host

// DefaultRunnerGroupPlan 返回 Python 双 Supervisor 的缺省分组计划
//
// 内置组加载内置插件目录与第三方目录，但只把内置目录作为受信目录，并按
// trusted_or_adapter 过滤，从而接管 adapter 类插件；第三方组只加载第三方目录并排除 adapter。
// 返回的分组顺序由 StartRunnerGroups 再次保证内置优先。
func DefaultRunnerGroupPlan(projectRoot, instanceRoot string, base StartRunnerOptions) []RunnerGroupSpec {
	builtin := DefaultBuiltinPluginDir(projectRoot)
	thirdParty := DefaultThirdPartyPluginDir(instanceRoot)
	plan := make([]RunnerGroupSpec, 0, 2)
	if builtin != "" || thirdParty != "" {
		dirs := make([]string, 0, 2)
		trusted := make([]string, 0, 1)
		if builtin != "" {
			dirs = append(dirs, builtin)
			trusted = append(trusted, builtin)
		}
		if thirdParty != "" {
			dirs = append(dirs, thirdParty)
		}
		options := base
		options.Group = RunnerGroupBuiltin
		options.PluginDirs = dirs
		options.TrustedPluginDirs = trusted
		options.PluginTypeFilter = "trusted_or_adapter"
		plan = append(plan, RunnerGroupSpec{Group: RunnerGroupBuiltin, Options: options})
	}
	if thirdParty != "" {
		options := base
		options.Group = RunnerGroupThirdParty
		options.PluginDirs = []string{thirdParty}
		options.TrustedPluginDirs = nil
		options.PluginTypeFilter = "not_adapter"
		plan = append(plan, RunnerGroupSpec{Group: RunnerGroupThirdParty, Options: options})
	}
	return plan
}
