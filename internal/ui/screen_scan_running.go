package ui

func showScanRunning(s *state, tasks []scanTask, onBack func()) {
	showSyncFlow(s, tasks, onBack)
}
