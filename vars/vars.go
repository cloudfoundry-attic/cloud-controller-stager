package vars

import "strings"

type StringList map[string]struct{}

func (v StringList) Set(arg string) error {
	v[arg] = struct{}{}
	return nil
}

func (v StringList) String() string {
	return strings.Join(v.toList(), ",")
}

func (v StringList) Get() interface{} {
	return v.toList()
}

func (v StringList) toList() []string {
	var result []string
	for k, _ := range v {
		result = append(result, k)
	}
	return result
}
