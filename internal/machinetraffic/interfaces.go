package machinetraffic

import (
	"errors"
	"slices"
	"strings"
)

type interfaceInfo struct {
	physical, bridge bool
	lower            []string
}

func selectInterfaces(names []string, inspect func(string) (interfaceInfo, error)) ([]string, error) {
	selected, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || visiting[name] {
			return errors.New("invalid network interface")
		}
		if selected[name] {
			return nil
		}
		visiting[name] = true
		defer delete(visiting, name)
		info, err := inspect(name)
		if err != nil {
			return err
		}
		if len(info.lower) > 0 {
			if info.bridge && len(info.lower) > 1 {
				return errors.New("ambiguous bridge uplink")
			}
			for _, lower := range info.lower {
				if err := visit(lower); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.physical {
			return errors.New("virtual interface cannot be a billing interface")
		}
		selected[name] = true
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	result := []string{}
	for name := range selected {
		result = append(result, name)
	}
	slices.Sort(result)
	if len(result) == 0 {
		return nil, errors.New("no external interfaces")
	}
	return result, nil
}
