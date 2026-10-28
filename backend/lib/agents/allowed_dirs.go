package agents

import (
	"os"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

type workflowDirs struct {
	mod             time.Time
	runDirs, ro, rw []string
}

var workflowDirsCache sync.Map // workflow.yaml path -> workflowDirs

// loadWorkflowDirs returns the run_dirs and mount_dirs a workflow.yaml
// declares; unreadable definitions contribute nothing (fail closed).
func loadWorkflowDirs(path string) (workflowDirs, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return workflowDirs{}, false
	}
	if v, ok := workflowDirsCache.Load(path); ok {
		if d := v.(workflowDirs); d.mod.Equal(info.ModTime()) {
			return d, true
		}
	}
	defn, err := workflowspec.LoadDefinition(path)
	if err != nil || defn == nil {
		return workflowDirs{}, false
	}
	d := workflowDirs{mod: info.ModTime(), runDirs: defn.RunDirs, ro: defn.MountDirs.ReadOnly, rw: defn.MountDirs.ReadWrite}
	workflowDirsCache.Store(path, d)
	return d, true
}

// AllowedDirs is the union of the directories the given agents (and the
// workflow definitions of workflow agents) may use as run dirs: their
// run_dirs and writable mount_dirs, plus readonly mounts when write is false.
func AllowedDirs(list []*agentspec.Agent, write bool) []string {
	var dirs []string
	for _, a := range list {
		if a == nil {
			continue
		}
		dirs = append(dirs, a.Config.RunDirs...)
		dirs = append(dirs, a.Config.MountDirs.ReadWrite...)
		if !write {
			dirs = append(dirs, a.Config.MountDirs.ReadOnly...)
		}
		if a.WorkflowPath != "" {
			if d, ok := loadWorkflowDirs(a.WorkflowPath); ok {
				dirs = append(dirs, d.runDirs...)
				dirs = append(dirs, d.rw...)
				if !write {
					dirs = append(dirs, d.ro...)
				}
			}
		}
	}
	return dirs
}

// ReadonlyDirs lists the readonly mount dirs of the agents and workflows;
// they veto writes even beneath a writable root.
func ReadonlyDirs(list []*agentspec.Agent) []string {
	var dirs []string
	for _, a := range list {
		if a == nil {
			continue
		}
		dirs = append(dirs, a.Config.MountDirs.ReadOnly...)
		if a.WorkflowPath != "" {
			if d, ok := loadWorkflowDirs(a.WorkflowPath); ok {
				dirs = append(dirs, d.ro...)
			}
		}
	}
	return dirs
}
