package agents

import (
	"fmt"
	"os"
	"path/filepath"
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
	d, ok, _ := loadWorkflowDirsErr(path)
	return d, ok
}

// loadWorkflowDirsErr is loadWorkflowDirs that also reports why an existing
// definition could not be loaded (a missing file is not an error).
func loadWorkflowDirsErr(path string) (workflowDirs, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return workflowDirs{}, false, nil
	}
	if v, ok := workflowDirsCache.Load(path); ok {
		if d := v.(workflowDirs); d.mod.Equal(info.ModTime()) {
			return d, true, nil
		}
	}
	defn, err := workflowspec.LoadDefinition(path)
	if err != nil {
		return workflowDirs{}, false, fmt.Errorf("workflow %s: %w", path, err)
	}
	if defn == nil {
		return workflowDirs{}, false, nil
	}
	d := workflowDirs{mod: info.ModTime(), runDirs: defn.RunDirs, ro: defn.MountDirs.ReadOnly, rw: defn.MountDirs.ReadWrite}
	workflowDirsCache.Store(path, d)
	return d, true, nil
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
// they veto writes even beneath a writable root. A non-absolute entry cannot
// be mounted read-only, so it is an error naming the entry (the absolute
// entries are still returned).
func ReadonlyDirs(list []*agentspec.Agent) ([]string, error) {
	var dirs []string
	var bad error
	add := func(ds []string) {
		for _, d := range ds {
			if !filepath.IsAbs(d) {
				if bad == nil {
					bad = fmt.Errorf("mount readonly directory must be an absolute path: %q", d)
				}
				continue
			}
			dirs = append(dirs, d)
		}
	}
	for _, a := range list {
		if a == nil {
			continue
		}
		add(a.Config.MountDirs.ReadOnly)
		if a.WorkflowPath != "" {
			d, ok, err := loadWorkflowDirsErr(a.WorkflowPath)
			if err != nil && bad == nil {
				// Its readonly mounts are unknown: fail closed.
				bad = err
			}
			if ok {
				add(d.ro)
			}
		}
	}
	return dirs, bad
}
