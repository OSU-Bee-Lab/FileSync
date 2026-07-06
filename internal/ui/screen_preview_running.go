package ui

func showPreviewRunning(s *state, tasks []previewTask, onBack func()) {
	showSyncFlow(s, tasks, onBack)
}
