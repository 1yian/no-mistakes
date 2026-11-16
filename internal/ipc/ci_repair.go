package ipc

type AdmitCIRepairParams struct {
	RunID   string `json:"run_id"`
	RepoID  string `json:"repo_id"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	PRURL   string `json:"pr_url"`
}

type AdmitCIRepairResult struct {
	RunID       string `json:"run_id"`
	HeadSHA     string `json:"head_sha"`
	State       string `json:"state"`
	SnapshotDir string `json:"snapshot_dir"`
}
