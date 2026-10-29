package sysinfo

import "testing"

// Real -listnetworkserviceorder output, with Wi-Fi deliberately not last: the
// old parser matched "Device: en0," (the listing ends with "en0)") and therefore
// always returned the last service, silently setting the proxy on a dead
// interface on any Mac that has Thunderbolt Bridge or iPhone USB after Wi-Fi.
const listing = `An asterisk (*) denotes that a network service is disabled.
(1) AX88179A
(Hardware Port: AX88179A, Device: en5)

(2) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)

(3) Thunderbolt Bridge
(Hardware Port: Thunderbolt Bridge, Device: bridge0)

(4) * iPhone USB
(Hardware Port: iPhone USB, Device: en7)
`

func TestParseServiceOrderMatchesDevice(t *testing.T) {
	cases := []struct{ iface, want string }{
		{"en0", "Wi-Fi"},
		{"en5", "AX88179A"},
		{"bridge0", "Thunderbolt Bridge"},
		{"en7", "iPhone USB"},
	}
	for _, c := range cases {
		if got := parseServiceOrder(listing, c.iface); got != c.want {
			t.Errorf("iface %s -> %q, want %q", c.iface, got, c.want)
		}
	}
}

func TestParseServiceOrderFallsBackToFirstEnabled(t *testing.T) {
	if got := parseServiceOrder(listing, "en9"); got != "AX88179A" {
		t.Errorf("unknown interface -> %q", got)
	}
	if got := parseServiceOrder(listing, ""); got != "AX88179A" {
		t.Errorf("no interface -> %q", got)
	}
	only := "(1) * Wi-Fi\n(Hardware Port: Wi-Fi, Device: en0)\n"
	if got := parseServiceOrder(only, "en9"); got != "" {
		t.Errorf("all-disabled fallback -> %q, want empty", got)
	}
}

func TestParseFingerprintAcceptsBothSpellings(t *testing.T) {
	// LibreSSL (the /usr/bin/openssl on macOS) spells the label lower-case.
	const want = "E3BDA10791F355F8A7EA65782786D8807174E69E"
	for _, in := range []string{
		"sha1 Fingerprint=E3:BD:A1:07:91:F3:55:F8:A7:EA:65:78:27:86:D8:80:71:74:E6:9E",
		"SHA1 Fingerprint=e3:bd:a1:07:91:f3:55:f8:a7:ea:65:78:27:86:d8:80:71:74:e6:9e",
	} {
		got, err := parseFingerprint(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Errorf("%q -> %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "(null)", "sha1 Fingerprint=AA:BB"} {
		if _, err := parseFingerprint(bad); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}
