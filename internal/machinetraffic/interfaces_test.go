package machinetraffic

import (
	"errors"
	"reflect"
	"testing"
)

func TestExternalInterfacesAvoidVirtualDoubleCounting(t *testing.T) {
	graph := map[string]interfaceInfo{
		"eth0": {billable: true}, "eth1": {billable: true},
		"bond0": {lower: []string{"eth0", "eth1"}}, "vlan10": {lower: []string{"bond0"}},
		"docker0": {bridge: true, lower: []string{"veth0", "veth1"}}, "tun0": {},
		"bridge0": {bridge: true, lower: []string{"eth0"}}, "cycle0": {lower: []string{"cycle1"}}, "cycle1": {lower: []string{"cycle0"}},
	}
	inspect := func(name string) (interfaceInfo, error) {
		v, ok := graph[name]
		if !ok {
			return interfaceInfo{}, errors.New("missing")
		}
		return v, nil
	}
	for _, names := range [][]string{{"vlan10", "bond0", "eth0", "eth1"}, {"eth0", "eth1", "eth0"}} {
		got, err := selectInterfaces(names, inspect)
		if err != nil || !reflect.DeepEqual(got, []string{"eth0", "eth1"}) {
			t.Fatalf("double counted interfaces: %v %v", got, err)
		}
	}
	got, err := selectInterfaces([]string{"bridge0"}, inspect)
	if err != nil || !reflect.DeepEqual(got, []string{"eth0"}) {
		t.Fatal("single bridge uplink was not resolved")
	}
	for _, names := range [][]string{{"docker0"}, {"tun0"}, {"missing"}, {"../eth0"}, {"cycle0"}, {}} {
		if _, err := selectInterfaces(names, inspect); err == nil {
			t.Fatalf("ambiguous or virtual interface accepted: %v", names)
		}
	}
}
