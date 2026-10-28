package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// resolveAndValidateNamespacedPath handles workspace/file paths targeting a session
// namespace ("tmp" or "session"): explicit forms (/<ns>, .<ns>, /<ns>/...) and relative
// forms (<ns>, <ns>/...). It returns handled=false when cleanReq does not target the namespace.
func resolveAndValidateNamespacedPath(ns string, runDir, reqPath, scope, sessionID string) (bool, string, string, string, error) {
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := filepath.Clean(runDir)
	base := GetSessionScopedBaseDir(ns, sessionID)

	evalBase := base
	if eb, evalErr := filepath.EvalSymlinks(base); evalErr == nil {
		evalBase = eb
	}

	evalRunDir := cleanRunDir
	if er, evalErr := filepath.EvalSymlinks(cleanRunDir); evalErr == nil {
		evalRunDir = er
	}

	nsPrefix := "/" + ns

	// Explicit namespace path (/<ns>, .<ns>, /<ns>/...)
	if isExplicit, sub := ResolveSessionScopedPath(cleanReq, sessionID, ns); isExplicit {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		target := filepath.Join(base, sub)
		relLex, errLex := filepath.Rel(base, target)
		if errLex != nil || strings.HasPrefix(relLex, "..") {
			return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
		}

		evalTarget, errEval := filepath.EvalSymlinks(target)
		if errEval == nil {
			relEval, errRel := filepath.Rel(evalBase, evalTarget)
			if errRel != nil || strings.HasPrefix(relEval, "..") {
				return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
		}

		canonicalAuth := nsPrefix
		if sub != "" {
			canonicalAuth = nsPrefix + "/" + sub
		}
		return true, target, ns, canonicalAuth, nil
	}

	// Relative namespace path (<ns>, <ns>/...)
	if isRelNs, sub := RelativeScopedPrefixedPath(cleanReq, sessionID, ns); isRelNs {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		canonicalAuth := nsPrefix
		if sub != "" {
			canonicalAuth = nsPrefix + "/" + sub
		}

		resolveAsNs := func() (string, string, string, error) {
			target := filepath.Join(base, sub)
			relLex, errLex := filepath.Rel(base, target)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(target)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalBase, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return "", "", "", errors.New("access denied: path escapes temporary directory boundary")
				}
			}
			return target, ns, canonicalAuth, nil
		}

		resolveAsWs := func() (string, string, string, error) {
			target := filepath.Join(cleanRunDir, cleanReq)
			relLex, errLex := filepath.Rel(cleanRunDir, target)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return "", "", "", errors.New("access denied: path escapes workspace boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(target)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return "", "", "", errors.New("access denied: path escapes workspace boundary")
				}
			}
			return target, "", cleanReq, nil
		}

		if scope == ns {
			target, resolvedNs, auth, err := resolveAsNs()
			return true, target, resolvedNs, auth, err
		}

		if scope == "workspace" {
			target, resolvedNs, auth, err := resolveAsWs()
			return true, target, resolvedNs, auth, err
		}

		// scope unprovided or invalid -> auto disambiguation
		isRunDirNsBase := (cleanRunDir == base || cleanRunDir == evalBase || evalRunDir == evalBase)
		if isRunDirNsBase {
			target, resolvedNs, auth, err := resolveAsNs()
			return true, target, resolvedNs, auth, err
		}

		// runDir is project workspace
		wsTarget := filepath.Join(cleanRunDir, cleanReq)
		if _, err := os.Stat(wsTarget); err == nil {
			relLex, errLex := filepath.Rel(cleanRunDir, wsTarget)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(wsTarget)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
				}
			}
			return true, wsTarget, "", cleanReq, nil
		}

		// wsTarget does not exist, check if session namespace target exists
		nsTarget := filepath.Join(base, sub)
		if _, err := os.Stat(nsTarget); err == nil {
			relLex, errLex := filepath.Rel(base, nsTarget)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(nsTarget)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalBase, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
				}
			}
			return true, nsTarget, ns, canonicalAuth, nil
		}

		// Neither exists: fall back to workspace target so os.Stat returns standard 404
		relLex, errLex := filepath.Rel(cleanRunDir, wsTarget)
		if errLex != nil || strings.HasPrefix(relLex, "..") {
			return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
		}
		return true, wsTarget, "", cleanReq, nil
	}

	return false, "", "", "", nil
}

// ResolveAndValidatePath resolves a workspace/file request path to an absolute
// path inside the chat roots (run dir, tmp, session), returning the namespace
// ("tmp", "session" or "" for the workspace) and the canonical auth path.
func ResolveAndValidatePath(runDir, reqPath, scope, sessionID string) (targetAbs string, ns string, authPath string, err error) {
	// Namespaced paths (/tmp, /session and their relative forms)
	for _, namespace := range []string{"tmp", "session"} {
		handled, target, resolvedNs, auth, err := resolveAndValidateNamespacedPath(namespace, runDir, reqPath, scope, sessionID)
		if handled {
			return target, resolvedNs, auth, err
		}
	}

	// Case 3: Ordinary workspace relative or absolute path (scope ignored)
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := filepath.Clean(runDir)

	evalRunDir := cleanRunDir
	if er, evalErr := filepath.EvalSymlinks(cleanRunDir); evalErr == nil {
		evalRunDir = er
	}

	var targetAbsPath string
	if filepath.IsAbs(cleanReq) {
		targetAbsPath = cleanReq
	} else {
		targetAbsPath = filepath.Join(cleanRunDir, cleanReq)
	}

	relLex, errLex := filepath.Rel(cleanRunDir, targetAbsPath)
	if errLex != nil || strings.HasPrefix(relLex, "..") {
		return "", "", "", errors.New("access denied: path escapes workspace boundary")
	}
	evalTarget, errEval := filepath.EvalSymlinks(targetAbsPath)
	if errEval == nil {
		relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
		if errRel != nil || strings.HasPrefix(relEval, "..") {
			return "", "", "", errors.New("access denied: path escapes workspace boundary")
		}
	}

	return targetAbsPath, "", cleanReq, nil
}

// AuthCandidate is a possible (namespace, canonical auth path) outcome of
// ResolveAndValidatePath.
type AuthCandidate struct {
	Ns   string
	Path string
}

// AuthCandidates lists, without touching the filesystem, the outcomes
// ResolveAndValidatePath can produce for reqPath: one for explicit or scoped
// requests and two (namespace or workspace) for the ambiguous relative
// "tmp/x" / "session/x" forms, which the file system decides. The backend
// authorizes against these before it asks the runner for anything.
func AuthCandidates(reqPath, scope, sessionID string) []AuthCandidate {
	clean := filepath.Clean(reqPath)
	for _, ns := range []string{"tmp", "session"} {
		canon := func(sub string) string {
			sub = filepath.Clean(sub)
			if sub == "" || sub == "." {
				return "/" + ns
			}
			return "/" + ns + "/" + sub
		}
		if ok, sub := ResolveSessionScopedPath(clean, sessionID, ns); ok {
			return []AuthCandidate{{ns, canon(sub)}}
		}
		if ok, sub := RelativeScopedPrefixedPath(clean, sessionID, ns); ok {
			nsC := AuthCandidate{ns, canon(sub)}
			wsC := AuthCandidate{"", clean}
			switch scope {
			case ns:
				return []AuthCandidate{nsC}
			case "workspace":
				return []AuthCandidate{wsC}
			}
			return []AuthCandidate{nsC, wsC}
		}
	}
	return []AuthCandidate{{"", clean}}
}
