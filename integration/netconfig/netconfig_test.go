// Copyright 2018 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package integration_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rtr7/router7/internal/netconfig"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
)

const goldenInterfaces = `
{
  "interfaces":[
    {
      "hardware_addr": "02:73:53:00:ca:fe",
      "name": "uplink0"
    },
    {
      "hardware_addr": "02:73:53:00:b0:0c",
      "spoof_hardware_addr": "02:73:53:00:b0:aa",
      "name": "lan0",
      "addr": "192.168.42.1/24"
    }
  ]
}
`

const goldenPortForwardings = `
{
  "forwardings":[
    {
      "port": "8080",
      "dest_addr": "192.168.42.23",
      "dest_port": "9999"
    },
    {
      "port": "8040-8060",
      "dest_addr": "192.168.42.99",
      "dest_port": "8040-8060"
    },
    {
      "proto": "udp",
      "port": "53",
      "dest_addr": "192.168.42.99",
      "dest_port": "53"
    }
  ]
}
`

const additionalPortForwardings = `
{
  "forwardings":[
    {
      "port": "8080",
      "dest_addr": "192.168.42.23",
      "dest_port": "9999"
    },
    {
      "port": "8045",
      "dest_addr": "192.168.42.22",
      "dest_port": "8045"
    },
    {
      "port": "8040-8060",
      "dest_addr": "192.168.42.99",
      "dest_port": "8040-8060"
    },
    {
      "proto": "udp",
      "port": "53",
      "dest_addr": "192.168.42.99",
      "dest_port": "53"
    }
  ]
}
`

const goldenDhcp4 = `
{
  "valid_until":"2018-05-18T23:46:04.429895261+02:00",
  "client_ip":"85.195.207.62",
  "subnet_mask":"255.255.255.128",
  "router":"85.195.207.1",
  "dns":[
    "77.109.128.2",
    "213.144.129.20"
  ]
}
`

const goldenDhcp6 = `
{
  "valid_until":"0001-01-01T00:00:00Z",
  "prefixes":[
    {"IP":"2a02:168:4a00::","Mask":"////////AAAAAAAAAAAAAA=="}
  ],
  "dns":[
    "2001:1620:2777:1::10",
    "2001:1620:2777:2::20"
  ]
}
`

func TestNetconfig(t *testing.T) {
	if os.Getenv("HELPER_PROCESS") == "1" {
		tmp, err := ioutil.TempDir("", "router7")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmp)

		pf := goldenPortForwardings
		if os.Getenv("ADDITIONAL_PORT_FORWARDINGS") == "1" {
			pf = additionalPortForwardings
		}
		for _, golden := range []struct {
			filename, content string
		}{
			{"dhcp4/wire/lease.json", goldenDhcp4},
			{"dhcp6/wire/lease.json", goldenDhcp6},
			{"interfaces.json", goldenInterfaces},
			{"portforwardings.json", pf},
		} {
			if err := os.MkdirAll(filepath.Join(tmp, filepath.Dir(golden.filename)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := ioutil.WriteFile(filepath.Join(tmp, golden.filename), []byte(golden.content), 0600); err != nil {
				t.Fatal(err)
			}
		}

		if err := os.MkdirAll(filepath.Join(tmp, "root", "etc"), 0755); err != nil {
			t.Fatal(err)
		}

		if err := os.MkdirAll(filepath.Join(tmp, "root", "tmp"), 0755); err != nil {
			t.Fatal(err)
		}

		netconfig.DefaultCounterObj = &nftables.CounterObj{Packets: 23, Bytes: 42}
		if err := netconfig.Apply(tmp, filepath.Join(tmp, "root")); err != nil {
			t.Fatalf("netconfig.Apply: %v", err)
		}

		// Apply twice to ensure the absence of errors when dealing with
		// already-configured interfaces, addresses, routes, … (and ensure
		// nftables rules are replaced, not appendend to).
		netconfig.DefaultCounterObj = &nftables.CounterObj{Packets: 0, Bytes: 0}
		if err := netconfig.Apply(tmp, filepath.Join(tmp, "root")); err != nil {
			t.Fatalf("netconfig.Apply: %v", err)
		}

		b, err := ioutil.ReadFile(filepath.Join(tmp, "root", "tmp", "resolv.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.TrimSpace(string(b)), "nameserver 192.168.42.1"; got != want {
			t.Errorf("/tmp/resolv.conf: got %q, want %q", got, want)
		}

		return
	}
	const ns = "ns3" // name of the network namespace to use for this test

	add := exec.Command("ip", "netns", "add", ns)
	add.Stderr = os.Stderr
	if err := add.Run(); err != nil {
		t.Fatalf("%v: %v", add.Args, err)
	}
	defer exec.Command("ip", "netns", "delete", ns).Run()

	nsSetup := []*exec.Cmd{
		exec.Command("ip", "netns", "exec", ns, "ip", "link", "add", "dummy0", "type", "dummy"),
		exec.Command("ip", "netns", "exec", ns, "ip", "link", "add", "lan0", "type", "dummy"),
		exec.Command("ip", "netns", "exec", ns, "ip", "link", "set", "dummy0", "address", "02:73:53:00:ca:fe"),
		exec.Command("ip", "netns", "exec", ns, "ip", "link", "set", "lan0", "address", "02:73:53:00:b0:0c"),
	}

	for _, cmd := range nsSetup {
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v", cmd.Args, err)
		}
	}

	cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestNetconfig$")
	cmd.Env = append(os.Environ(), "HELPER_PROCESS=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	link, err := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", "lan0").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(link), "link/ether 02:73:53:00:b0:aa") {
		t.Errorf("lan0 MAC address is not 02:73:53:00:b0:aa")
	}

	addrs, err := exec.Command("ip", "netns", "exec", ns, "ip", "address", "show", "dev", "uplink0").Output()
	if err != nil {
		t.Fatal(err)
	}

	addrRe := regexp.MustCompile(`(?m)^\s*inet 85.195.207.62/25 brd 85.195.207.127 scope global uplink0$`)
	if !addrRe.MatchString(string(addrs)) {
		t.Fatalf("regexp %s does not match %s", addrRe, string(addrs))
	}

	addrsLan, err := exec.Command("ip", "netns", "exec", ns, "ip", "address", "show", "dev", "lan0").Output()
	if err != nil {
		t.Fatal(err)
	}
	addr6Re := regexp.MustCompile(`(?m)^\s*inet6 2a02:168:4a00::1/64 scope global\s*$`)
	if !addr6Re.MatchString(string(addrsLan)) {
		t.Fatalf("regexp %s does not match %s", addr6Re, string(addrsLan))
	}

	wantRoutes := []string{
		"default via 85.195.207.1 proto dhcp src 85.195.207.62 ",
		"85.195.207.0/25 proto kernel scope link src 85.195.207.62 ",
		"85.195.207.1 proto dhcp scope link src 85.195.207.62",
	}

	routes, err := ipLines("netns", "exec", ns, "ip", "route", "show", "dev", "uplink0")
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(routes, wantRoutes); diff != "" {
		t.Fatalf("routes: diff (-got +want):\n%s", diff)
	}

	rules, err := ipLines("netns", "exec", ns, "nft", "list", "ruleset")
	if err != nil {
		t.Fatal(err)
	}
	for n, rule := range rules {
		t.Logf("rule %d: %s", n, rule)
	}
	if len(rules) < 2 {
		t.Fatalf("nftables rules not found")
	}
	wantRules := []string{
		`table ip nat {`,
		`	chain prerouting {`,
		`		type nat hook prerouting priority 0; policy accept;`,
		`		iifname "uplink0" udp dport domain dnat to 192.168.42.99:domain`,
		`		iifname "uplink0" tcp dport 8040-8060 dnat to 192.168.42.99:8040-8060`,
		`		iifname "uplink0" tcp dport http-alt dnat to 192.168.42.23:9999`,
		`	}`,
		``,
		`	chain postrouting {`,
		`		type nat hook postrouting priority 100; policy accept;`,
		`		oifname "uplink0" masquerade`,
		`	}`,
		`}`,
		`table ip filter {`,
		`   counter fwded {`,
		`       packets 23 bytes 42`,
		`   }`,
		``,
		`	chain forward {`,
		`		type filter hook forward priority 0; policy accept;`,
		`		counter name "fwded"`,
		`		oifname "uplink0" tcp flags syn tcp option maxseg size set rt mtu`,
		`	}`,
		`}`,
		`table ip6 filter {`,
		`   counter fwded {`,
		`       packets 23 bytes 42`,
		`   }`,
		``,
		`	chain forward {`,
		`		type filter hook forward priority 0; policy accept;`,
		`		counter name "fwded"`,
		`		oifname "uplink0" tcp flags syn tcp option maxseg size set rt mtu`,
		`	}`,
		`}`,
	}
	opts := []cmp.Option{
		cmp.Transformer("formatting", func(line string) string {
			return strings.TrimSpace(strings.Replace(line, "dnat to", "dnat", -1))
		}),
	}

	if diff := cmp.Diff(rules, wantRules, opts...); diff != "" {
		t.Fatalf("unexpected nftables rules: diff (-got +want):\n%s", diff)
	}

	cmd = exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestNetconfig$")
	cmd.Env = append(os.Environ(), "HELPER_PROCESS=1", "ADDITIONAL_PORT_FORWARDINGS=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	rules, err = ipLines("netns", "exec", ns, "nft", "list", "ruleset")
	if err != nil {
		t.Fatal(err)
	}
	for n, rule := range rules {
		t.Logf("rule %d: %s", n, rule)
	}
	if len(rules) < 2 {
		t.Fatalf("nftables rules not found")
	}

	wantRules = []string{
		`table ip nat {`,
		`	chain prerouting {`,
		`		type nat hook prerouting priority 0; policy accept;`,
		`		iifname "uplink0" udp dport domain dnat to 192.168.42.99:domain`,
		`		iifname "uplink0" tcp dport 8040-8060 dnat to 192.168.42.99:8040-8060`,
		`		iifname "uplink0" tcp dport 8045 dnat to 192.168.42.22:8045`,
		`		iifname "uplink0" tcp dport http-alt dnat to 192.168.42.23:9999`,
		`	}`,
		``,
		`	chain postrouting {`,
		`		type nat hook postrouting priority 100; policy accept;`,
		`		oifname "uplink0" masquerade`,
		`	}`,
		`}`,
		`table ip filter {`,
		`   counter fwded {`,
		`       packets 23 bytes 42`,
		`   }`,
		``,
		`	chain forward {`,
		`		type filter hook forward priority 0; policy accept;`,
		`		counter name "fwded"`,
		`		oifname "uplink0" tcp flags syn tcp option maxseg size set rt mtu`,
		`	}`,
		`}`,
		`table ip6 filter {`,
		`   counter fwded {`,
		`       packets 23 bytes 42`,
		`   }`,
		``,
		`	chain forward {`,
		`		type filter hook forward priority 0; policy accept;`,
		`		counter name "fwded"`,
		`		oifname "uplink0" tcp flags syn tcp option maxseg size set rt mtu`,
		`	}`,
		`}`,
	}
	if diff := cmp.Diff(rules, wantRules, opts...); diff != "" {
		t.Fatalf("unexpected nftables rules: diff (-got +want):\n%s", diff)
	}
}

func ipLines(args ...string) ([]string, error) {
	cmd := exec.Command("ip", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %v", cmd.Args, err)
	}
	outstr := string(out)
	for strings.Contains(outstr, "  ") {
		outstr = strings.Replace(outstr, "  ", " ", -1)
	}

	return strings.Split(strings.TrimSpace(outstr), "\n"), nil
}
