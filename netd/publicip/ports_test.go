package publicip

import "testing"

func TestSplitEntryPort(t *testing.T) {
	for entry, want := range map[string]struct {
		host string
		port uint16
	}{
		"smtp.example.com:25":    {"smtp.example.com", 25},
		"*.example.com:25":       {"*.example.com", 25},
		"*:25":                   {"*", 25},
		"203.0.113.7:25":         {"203.0.113.7", 25},
		"203.0.113.0/24:25":      {"203.0.113.0/24", 25},
		"[2001:db8::1]:25":       {"2001:db8::1", 25},
		"[2001:db8::1]":          {"2001:db8::1", 0},
		"2001:db8::/32:25":       {"2001:db8::/32", 25},
		"2001:db8::1":            {"2001:db8::1", 0},
		"2001:db8::25":           {"2001:db8::25", 0}, // an address, not port 25
		"2001:db8::/32":          {"2001:db8::/32", 0},
		"smtp.example.com":       {"smtp.example.com", 0},
		"smtp.example.com:0":     {"smtp.example.com:0", 0},   // not a port: kept whole
		"smtp.example.com:abc":   {"smtp.example.com:abc", 0}, // same
		"smtp.example.com:70000": {"smtp.example.com:70000", 0},
		":25":                    {":25", 0},
		"[2001:db8::1]:x":        {"[2001:db8::1]:x", 0},
	} {
		host, port := SplitEntryPort(entry)
		if host != want.host || port != want.port {
			t.Errorf("SplitEntryPort(%q) = %q, %d; want %q, %d", entry, host, port, want.host, want.port)
		}
	}
}

func TestEntryPortOK(t *testing.T) {
	for entry, want := range map[string]bool{
		"smtp.example.com:25": true, "smtp.example.com": true, "*:25": true, "[2001:db8::1]:25": true,
		"2001:db8::1": true, "2001:db8::25": true, "2001:db8::/32:25": true, "203.0.113.0/24:25": true,
		"smtp.example.com:abc": false, "smtp.example.com:0": false, "smtp.example.com:70000": false,
		"[2001:db8::1]:x": false, "[2001:db8::1": false, "2001:db8::/32:x": false,
	} {
		if got := EntryPortOK(entry); got != want {
			t.Errorf("EntryPortOK(%q) = %v, want %v", entry, got, want)
		}
	}
}

func TestAllowlistPorts(t *testing.T) {
	l := ParseAllowlist([]string{"api.example.com", "smtp.example.com:25", "*.mail.example.com:587", "203.0.113.0/24", "198.51.100.1:25", "[2001:db8::1]:25"})
	type q struct {
		name string
		port uint16
	}
	for c, want := range map[q][3]bool{ // Allows, AllowsPort, NamesPort
		{"api.example.com", 443}:     {true, true, false},
		{"api.example.com", 25}:      {true, true, false}, // on the list, but doesn't name 25: RefusedPort applies
		{"smtp.example.com", 25}:     {true, true, true},
		{"smtp.example.com", 587}:    {true, false, false}, // the entry names another port
		{"mx.mail.example.com", 587}: {true, true, true},
		{"mx.mail.example.com", 25}:  {true, false, false},
		{"other.example.com", 443}:   {false, false, false},
		{"203.0.113.9", 25}:          {true, true, false},
		{"198.51.100.1", 25}:         {true, true, true},
		{"198.51.100.1", 443}:        {true, false, false},
		{"2001:db8::1", 25}:          {true, true, true},
		{"::ffff:198.51.100.1", 25}:  {true, true, true},
	} {
		got := [3]bool{l.Allows(c.name), l.AllowsPort(c.name, c.port), l.NamesPort(c.name, c.port)}
		if got != want {
			t.Errorf("%s:%d: Allows/AllowsPort/NamesPort = %v, want %v", c.name, c.port, got, want)
		}
	}
	any := ParseAllowlist([]string{"*", "*:25", "0.0.0.0/0:25"})
	if !any.NamesPort("anything.example", 25) || !any.NamesPort("203.0.113.9", 25) || any.NamesPort("anything.example", 587) {
		t.Error(`"*:25" and "0.0.0.0/0:25" name port 25 for every name and IPv4 literal, and nothing else`)
	}
}

func TestRefusedPort(t *testing.T) {
	if RefusedPort(25) == "" {
		t.Error("port 25 must be refused by default")
	}
	for _, p := range []uint16{80, 443, 465, 587, 5432, 2525} {
		if why := RefusedPort(p); why != "" {
			t.Errorf("RefusedPort(%d) = %q, want allowed", p, why)
		}
	}
}
