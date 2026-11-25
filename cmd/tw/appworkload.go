package main

// How tw d1, kv, r2 and secret take the workload from the app in the
// current directory when --workload is absent (issue #524). loadApp stays
// the single source of the name, so these commands name the workload
// exactly as tw deploy does; this file only decides what to say when
// loadApp fails, instead of swallowing its error.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// appBaseName is the workload name an app implies before any wrangler
// environment: tailworks.jsonc "name", else the wrangler config's "name",
// else the directory name.
func appBaseName(twName, wranglerName, dir string) string {
	switch {
	case twName != "":
		return twName
	case wranglerName != "":
		return wranglerName
	}
	return filepath.Base(dir)
}

// appConfigPresent reports whether dir has an app config tailworks reads:
// tailworks.jsonc, a wrangler config, or the config `vite build` wrote.
func appConfigPresent(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "tailworks.jsonc")); err == nil {
		return true
	}
	if f, _ := findWranglerConfig(dir); f != "" {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(viteDeployConfig)))
	return err == nil
}

// appWorkloadErr is the error for `tw <cmdSub> …` when it needs the workload
// from the app in dir and loadApp(dir) failed with err. With no app config
// in dir it is "no app in the current directory"; with one, it is loadApp's
// own error (a schema problem, a bad wrangler config, …) plus a fix line to
// skip the app. extra is appended to the --workload fix (for example
// " --namespace <NAME>").
func appWorkloadErr(dir, cmdSub string, err error, extra string) error {
	skip := "tw " + cmdSub + " … --workload <workload>" + extra
	if !appConfigPresent(dir) {
		return fail("bad_request", "no app in the current directory to take the workload from").
			withFix("cd <app dir> && tw "+cmdSub+" …", skip)
	}
	var ce *cliErr
	if !errors.As(err, &ce) {
		ce = fail("bad_request", strings.TrimSpace(err.Error()))
	}
	out := *ce
	out.Fix = append(append([]string(nil), ce.Fix...), skip+"   (to skip the app in this directory)")
	return &out
}

// appWorkload is the workload of the app in the current directory, for
// `tw <cmdSub>` without --workload.
func appWorkload(cmdSub string) (string, error) {
	a, err := loadApp(".", "")
	if err != nil {
		return "", appWorkloadErr(".", cmdSub, err, "")
	}
	return a.Name, nil
}
