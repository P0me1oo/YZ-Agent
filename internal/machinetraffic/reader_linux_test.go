//go:build linux

package machinetraffic

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestBillingInterfacesContainerUplinks(t *testing.T) {
	root := t.TempDir()
	class := filepath.Join(root, "class", "net")
	if err := os.MkdirAll(class, 0700); err != nil {
		t.Fatal(err)
	}
	links := map[string]netlink.Link{}
	add := func(name, kind string, physical bool, master, peerNamespace int, lowers ...string) {
		t.Helper()
		device := filepath.Join(root, "devices", "virtual", "net", name)
		if physical {
			device = filepath.Join(root, "devices", "pci", "net", name)
		}
		if err := os.MkdirAll(device, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(device, filepath.Join(class, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(device, "type"), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if kind == "bridge" {
			if err := os.Mkdir(filepath.Join(device, "bridge"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, lower := range lowers {
			if err := os.Symlink(filepath.Join(class, lower), filepath.Join(device, "lower_"+lower)); err != nil {
				t.Fatal(err)
			}
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name, attrs.MasterIndex, attrs.NetNsID = name, master, peerNamespace
		links[name] = &netlink.GenericLink{LinkAttrs: attrs, LinkType: kind}
	}
	add("eth0", "veth", false, 0, 0)
	add("eth1", "veth", false, 0, 0)
	add("ens3", "device", true, 0, -1)
	add("ens4", "device", true, 0, -1)
	add("veth-host", "veth", false, 10, 0)
	add("veth-local", "veth", false, 0, -1)
	add("docker0", "bridge", false, 0, -1, "veth-host")
	add("br-mixed", "bridge", false, 0, -1, "ens3", "veth-host")
	add("br-uplink", "bridge", false, 0, -1, "ens3")
	add("bond0", "bond", false, 0, -1, "ens3", "ens4")
	add("vlan10", "vlan", false, 0, -1, "bond0")
	add("eth0.20", "vlan", false, 0, -1, "eth0")
	for _, kind := range []string{"tun", "wireguard", "dummy", "vxlan", "gretap", "macvlan"} {
		add(kind+"0", kind, false, 0, -1)
	}
	// 隧道声明了底层出口也不能沿着出口被接受。
	add("vxlan-lower", "vxlan", false, 0, -1, "eth0")
	add("unknown0", "unknown", false, 0, 0)
	add("lookup-error", "veth", false, 0, 0)
	delete(links, "lookup-error")
	lookup := func(name string) (netlink.Link, error) {
		if link, ok := links[name]; ok {
			return link, nil
		}
		return nil, errors.New("link unavailable")
	}
	for _, tt := range []struct {
		name  string
		names []string
		want  []string
	}{
		{"container dual stack", []string{"eth1", "eth0"}, []string{"eth0", "eth1"}},
		{"shared IPv4 IPv6 uplink", []string{"eth0", "eth0"}, []string{"eth0"}},
		{"explicit uplink", []string{"eth1"}, []string{"eth1"}},
		{"container VLAN deduplication", []string{"eth0.20", "eth0"}, []string{"eth0"}},
		{"hardware bond VLAN deduplication", []string{"vlan10", "bond0", "ens3"}, []string{"ens3", "ens4"}},
		{"hardware bridge", []string{"br-uplink"}, []string{"ens3"}},
		{"host container port", []string{"veth-host"}, nil},
		{"local veth pair", []string{"veth-local"}, nil},
		{"container bridge", []string{"docker0"}, nil},
		{"mixed bridge", []string{"br-mixed"}, nil},
		{"tunnel with container uplink", []string{"vxlan-lower"}, nil},
		{"unknown virtual device", []string{"unknown0"}, nil},
		{"netlink failure", []string{"lookup-error"}, nil},
		{"missing interface", []string{"missing0"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := billingInterfaces(class, tt.names, lookup)
			if tt.want == nil {
				if err == nil {
					t.Fatalf("unsupported interfaces accepted: %v", got)
				}
			} else if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("billing interfaces = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	for _, kind := range []string{"tun", "wireguard", "dummy", "vxlan", "gretap", "macvlan"} {
		t.Run("reject "+kind, func(t *testing.T) {
			if got, err := billingInterfaces(class, []string{kind + "0"}, lookup); err == nil {
				t.Fatalf("virtual interface accepted: %v", got)
			}
		})
	}
}
