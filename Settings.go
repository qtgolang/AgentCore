package main

// SetToolCallCount 设置工具调用最大次数 默认4096
func (a *Agent) SetToolCallCount(count int) *Agent {
	a.maxToolCallRounds = count
	return a
}
func (a *Agent) SetProviderOpenAI() *Agent {
	a.isOpenAiProvider = true
	return a
}
func (a *Agent) SetProviderAnthropic() *Agent {
	a.isOpenAiProvider = false
	return a
}

// SetSkillApproverFn 设置工具、技能 批准回调
func (a *Agent) SetSkillApproverFn(fn SkillApprover) *Agent {
	a.skillApprover = fn
	return a
}

// SetMonitorFn 监控回调
func (a *Agent) SetMonitorFn(fn MonitorFunc) *Agent {
	a.monitor = fn
	return a
}

// SetCancelOnNew 取消模式开关、开启后多线程请求，仅保留最后一次请求
func (a *Agent) SetCancelOnNew(open bool) *Agent {
	a.cancelOnNew = open
	return a
}
