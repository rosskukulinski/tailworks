package main

// `tw dev` reads .dev.vars (dotenv, as wrangler dev does) from the app
// directory and gives its values to the workload as secrets: tw dev's
// embedded mgmt stores them and its hostd fetches them on each (re)deploy
// through the same lease-fenced grant as production, so env.NAME behaves
// exactly as after `tw secret put`. Keep .dev.vars out of git (the
// templates' .gitignore does).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// DevVarsFile is the dotenv file `tw dev` reads from the app directory.
const DevVarsFile = ".dev.vars"

// readDevVars parses dir/.dev.vars; a missing file is no vars.
func readDevVars(dir string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, DevVarsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseDotenv(DevVarsFile, string(b))
}

// parseDotenv parses KEY=VALUE lines: blank lines and # comments are
// skipped, an optional `export ` prefix is allowed, "double quoted" values
// may span lines and take \n \r \t \" \\ escapes, 'single quoted' values
// are literal, and unquoted values end at " #" and are trimmed. Errors
// name file and the line, never its value. A name given twice keeps its
// last value.
func parseDotenv(file, s string) (map[string]string, error) {
	vars, err := dotenvVars(file, s)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		out[v.name] = v.value
	}
	return out, nil
}

// dotenvVar is one NAME=VALUE of a dotenv file, with the line it starts on.
type dotenvVar struct {
	name, value string
	line        int
}

// dotenvVars is parseDotenv's NAME=VALUE pairs in file order, repeats
// included.
func dotenvVars(file, s string) ([]dotenvVar, error) {
	var out []dotenvVar
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		ln := strings.TrimSpace(lines[i])
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		ln = strings.TrimPrefix(ln, "export ")
		k, v, ok := strings.Cut(ln, "=")
		k = strings.TrimSpace(k)
		if !ok || !mgmt.ValidSecretName(k) {
			return nil, fmt.Errorf("%s line %d: want NAME=VALUE with NAME a JS identifier (not TW_…)", file, i+1)
		}
		at := i + 1
		v = strings.TrimLeft(v, " \t")
		switch {
		case strings.HasPrefix(v, `"`):
			// Double quoted, possibly over several lines.
			body := v[1:]
			start := i
			for !closesDouble(body) {
				if i+1 >= len(lines) {
					return nil, fmt.Errorf("%s line %d: unterminated \"", file, start+1)
				}
				i++
				body += "\n" + lines[i]
			}
			end := closingDouble(body)
			uq, err := unescapeDouble(body[:end])
			if err != nil {
				return nil, fmt.Errorf("%s line %d: %v", file, start+1, err)
			}
			v = uq
		case strings.HasPrefix(v, "'"):
			end := strings.Index(v[1:], "'")
			if end < 0 {
				return nil, fmt.Errorf("%s line %d: unterminated '", file, i+1)
			}
			v = v[1 : 1+end]
		default:
			if j := strings.Index(v, " #"); j >= 0 {
				v = v[:j]
			}
			v = strings.TrimSpace(v)
		}
		out = append(out, dotenvVar{name: k, value: v, line: at})
	}
	return out, nil
}

func closingDouble(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func closesDouble(s string) bool { return closingDouble(s) >= 0 }

func unescapeDouble(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '"', '\\', '$':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

// syncDevVars makes the dev mgmt's secrets of the workload exactly
// .dev.vars: puts every value and removes names no longer in the file.
// It returns the names, sorted.
func syncDevVars(ctx context.Context, mc *mgmtapi.Client, tenantID, workload, dir string) ([]string, error) {
	vars, err := readDevVars(dir)
	if err != nil {
		return nil, err
	}
	base := "/v1/tenants/" + url.PathEscape(tenantID) + "/workloads/" + url.PathEscape(workload) + "/secrets"
	cur, err := listAll[mgmtapi.SecretMeta](ctx, mc, base, 0)
	if err != nil {
		return nil, err
	}
	for _, m := range cur {
		if _, keep := vars[m.Name]; !keep {
			if err := mc.Do(ctx, "DELETE", base+"/"+m.Name, nil, nil); err != nil {
				return nil, err
			}
		}
	}
	names := make([]string, 0, len(vars))
	for k, v := range vars {
		if len(v) > mgmt.SecretMaxValueBytes {
			return nil, fmt.Errorf("%s: %s is over %s bytes", DevVarsFile, k, strconv.Itoa(mgmt.SecretMaxValueBytes))
		}
		if err := mc.Do(ctx, "PUT", base+"/"+k, map[string]string{"value": v}, nil); err != nil {
			return nil, err
		}
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}
