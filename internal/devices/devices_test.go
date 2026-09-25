package devices

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	devs, err := Parse(strings.NewReader(
		`[{"id":"fontanna","type":"pump","url":"http://192.168.1.42"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Name != "fontanna" {
		t.Fatalf("got %+v", devs)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"typo in field": `[{"id":"a","type":"pump","ulr":"http://x"}]`,
		"bad id":        `[{"id":"A/b","type":"pump","url":"http://x"}]`,
		"duplicate id":  `[{"id":"a","type":"pump","url":"http://x"},{"id":"a","type":"pump","url":"http://y"}]`,
		"missing type":  `[{"id":"a","url":"http://x"}]`,
		"no scheme":     `[{"id":"a","type":"pump","url":"192.168.1.42"}]`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
