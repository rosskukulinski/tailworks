package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// tw doctor's human output. By default it is a summary: problems first,
// each with its fix, then one line per group of passing checks, with
// skipped checks only counted. -v prints every check's full detail (the
// pre-summary layout); --json is unchanged apart from each check's group.

// doctorMeta is a check's group and its short human label. fact, if set,
// pulls the one number or mode worth showing from a passing check's detail.
type doctorMeta struct {
	group string
	label string
	fact  func(detail string) string
}

// doctorGroups is the display order of the groups.
var doctorGroups = []string{
	"Install",
	"Tailnet",
	"Storage",
	"Isolation",
	"Linux host",
	"Other",
}

var (
	reDiskFree     = regexp.MustCompile(`^([\d.]+ \S+ free)`)
	reWorkerdVer   = regexp.MustCompile(`: workerd (\S+)`)
	reWorkerdAge   = regexp.MustCompile(`, (\d+ days?) old`)
	reSandboxKind  = regexp.MustCompile(`^(.+?) works? on `)
	reBeforeColon  = regexp.MustCompile(`^([^:;(]+)[:;(]`)
	reStoreTenants = regexp.MustCompile(`^(\d+ tenants?, \d+ stores?, \d+ queues?)`)
)

func submatch(re *regexp.Regexp) func(string) string {
	return func(d string) string {
		if m := re.FindStringSubmatch(d); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
}

var doctorMetas = map[string]doctorMeta{
	"api_key":                    {group: "Tailnet", label: "API key"},
	"binaries":                   {group: "Install", label: "daemon binaries", fact: binariesFact},
	"bundler":                    {group: "Install", label: "bundler"},
	"cgroups_v2":                 {group: "Linux host", label: "cgroups v2"},
	"containers":                 {group: "Isolation", label: "containers"},
	"data_disk":                  {group: "Storage", label: "disk", fact: submatch(reDiskFree)},
	"firecracker":                {group: "Linux host", label: "Firecracker"},
	"glibc":                      {group: "Install", label: "glibc"},
	"host_sockets":               {group: "Isolation", label: "host sockets"},
	"https_certs":                {group: "Tailnet", label: "HTTPS certs"},
	"install":                    {group: "Install", label: "installed by", fact: installFact},
	"kernel":                     {group: "Linux host", label: "kernel"},
	"kvm":                        {group: "Linux host", label: "KVM"},
	"landlock":                   {group: "Linux host", label: "Landlock"},
	"microvm_tier":               {group: "Linux host", label: "microVM tier"},
	"mint_mode":                  {group: "Tailnet", label: "mint", fact: mintFact},
	"node_budget":                {group: "Tailnet", label: "node budget", fact: nodeBudgetFact}, // nodebudget.go
	"node_egress":                {group: "Isolation", label: "Node egress"},
	"public_ingress":             {group: "Tailnet", label: "public ingress"},
	"sandbox":                    {group: "Isolation", label: "sandbox", fact: submatch(reSandboxKind)},
	"side_channels":              {group: "Isolation", label: "side channels"},
	"state_dir_writable":         {group: "Install", label: "state dir writable"},
	"store_sizes":                {group: "Storage", label: "stores", fact: submatch(reStoreTenants)},
	"system_tailscaled_not_used": {group: "Tailnet", label: "system tailscaled unused"},
	"systemd":                    {group: "Linux host", label: "systemd"},
	"transport":                  {group: "Tailnet", label: "transport", fact: submatch(reBeforeColon)},
	"tw_home_writable":           {group: "Install", label: "~/.tailworks writable"},
	"user_namespaces":            {group: "Linux host", label: "user namespaces"},
	"worker_internet":            {group: "Isolation", label: "Worker internet", fact: submatch(reBeforeColon)},
	"workerd":                    {group: "Install", label: "workerd", fact: submatch(reWorkerdVer)},
	"workerd_age":                {group: "Install", label: "workerd age", fact: submatch(reWorkerdAge)}, // doctor_workerdage.go
}

func binariesFact(d string) string {
	if strings.Contains(d, "not needed in single-process mode") {
		return "in-process"
	}
	return ""
}

func installFact(d string) string {
	switch {
	case strings.HasPrefix(d, "npm package") && strings.Contains(d, "(npx"):
		return "npx"
	case strings.HasPrefix(d, "npm package"):
		return "npm"
	case strings.HasPrefix(d, "release tarball"):
		return "install.sh"
	}
	return ""
}

func mintFact(d string) string {
	if strings.HasPrefix(d, "this machine is linked to ") {
		return "linked"
	}
	return submatch(reBeforeColon)(d)
}

// metaOf returns a check's group and label. Per-workload checks are named
// "<kind>:<workload>".
func metaOf(name string) doctorMeta {
	if m, ok := doctorMetas[name]; ok {
		return m
	}
	if kind, wl, ok := strings.Cut(name, ":"); ok {
		switch kind {
		case "funnel_attr":
			return doctorMeta{group: "Tailnet", label: "Funnel " + wl}
		case "public_probe":
			return doctorMeta{group: "Tailnet", label: "public " + wl}
		}
	}
	return doctorMeta{group: "Other", label: strings.ReplaceAll(name, "_", " ")}
}

var doctorMarks = map[string]string{"ok": "✓", "warn": "!", "fail": "✗", "skip": "-", "info": "i"}

func doctorHeader(w io.Writer, goos, arch string) {
	fmt.Fprintf(w, "tw doctor · tw %s · %s/%s\n", version, goos, arch)
}

// renderDoctorVerbose is every check with its full detail and fix.
func renderDoctorVerbose(w io.Writer, checks []check) {
	for _, c := range checks {
		fmt.Fprintf(w, "%s %-28s %s\n", doctorMarks[c.Status], c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "  %-28s fix: %s\n", "", c.Fix)
		}
	}
}

// renderDoctorSummary prints the counts, then failures and warnings with
// their (wrapped) detail and fix, then one line per group of passing and
// info checks. home is shortened to ~ and width is the terminal width.
func renderDoctorSummary(w io.Writer, checks []check, home string, width int) {
	n := map[string]int{}
	for _, c := range checks {
		n[c.Status]++
	}
	var counts []string
	add := func(st, one, many string) {
		if k := n[st]; k > 0 {
			word := many
			if k == 1 {
				word = one
			}
			counts = append(counts, fmt.Sprintf("%s %d %s", doctorMarks[st], k, word))
		}
	}
	add("fail", "failed", "failed")
	add("warn", "warning", "warnings")
	if k := n["ok"] + n["info"]; k > 0 {
		counts = append(counts, fmt.Sprintf("✓ %d passed", k))
	}
	if k := n["skip"]; k > 0 {
		counts = append(counts, fmt.Sprintf("%d skipped", k))
	}
	fmt.Fprintf(w, "%s\n", strings.Join(counts, " · "))

	short := func(s string) string {
		if home != "" && home != "/" {
			s = strings.ReplaceAll(s, home+"/", "~/")
			s = strings.ReplaceAll(s, home+" ", "~ ")
			if strings.HasSuffix(s, home) {
				s = strings.TrimSuffix(s, home) + "~"
			}
		}
		return s
	}

	// Problems: failures first, then warnings, in check order.
	labelW := 0
	for _, c := range checks {
		if c.Status == "fail" || c.Status == "warn" {
			labelW = max(labelW, len(metaOf(c.Name).label))
		}
	}
	indent := 2 + labelW + 2
	if n["fail"]+n["warn"] > 0 {
		fmt.Fprintln(w)
	}
	for _, st := range []string{"fail", "warn"} {
		for _, c := range checks {
			if c.Status != st {
				continue
			}
			lines := wrapText(short(c.Detail), width-indent)
			fmt.Fprintf(w, "%s %-*s  %s\n", doctorMarks[st], labelW, metaOf(c.Name).label, lines[0])
			for _, l := range lines[1:] {
				fmt.Fprintf(w, "%*s%s\n", indent, "", l)
			}
			if c.Fix != "" {
				fl := wrapText("fix: "+short(c.Fix), width-indent)
				for _, l := range fl {
					fmt.Fprintf(w, "%*s%s\n", indent, "", l)
				}
			}
		}
	}

	// Passing (and info) checks, one line per group.
	byGroup := map[string][]string{}
	for _, c := range checks {
		if c.Status != "ok" && c.Status != "info" {
			continue
		}
		m := metaOf(c.Name)
		item := m.label
		if m.fact != nil {
			if f := m.fact(c.Detail); f != "" && len(f) <= 40 {
				item += " (" + short(f) + ")"
			}
		}
		byGroup[m.group] = append(byGroup[m.group], item)
	}
	if len(byGroup) > 0 {
		fmt.Fprintln(w)
	}
	groupW := 0
	for g := range byGroup {
		groupW = max(groupW, len(g))
	}
	for _, g := range doctorGroups {
		items := byGroup[g]
		if len(items) == 0 {
			continue
		}
		gi := 2 + groupW + 2
		lines := wrapText(strings.Join(items, " · "), width-gi)
		fmt.Fprintf(w, "✓ %-*s  %s\n", groupW, g, lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%*s%s\n", gi, "", l)
		}
	}
	fmt.Fprintf(w, "\ntw doctor -v for every check's detail · --json for scripts\n")
}

// wrapText breaks s into lines of at most width runes at spaces (a word
// longer than width gets its own line).
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	var cur strings.Builder
	curN := 0
	for _, word := range strings.Fields(s) {
		wn := len([]rune(word))
		if curN > 0 && curN+1+wn > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curN = 0
		}
		if curN > 0 {
			cur.WriteByte(' ')
			curN++
		}
		cur.WriteString(word)
		curN += wn
	}
	if curN > 0 || len(lines) == 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// doctorWidth is stdout's terminal width, $COLUMNS, or 100.
func doctorWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		return c
	}
	return 100
}
